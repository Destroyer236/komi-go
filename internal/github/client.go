// Package github is the GitHub REST client. It implements conditional requests,
// tiered caching, bounded retries with jitter, 429/Retry-After handling,
// single-flight de-duplication and token hygiene across redirects.
package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"komi-go/internal/catalog"
	"komi-go/internal/ledger"
)

const (
	defaultAPIBase = "https://api.github.com"
	defaultWebBase = "https://github.com"
	apiVersion     = "2022-11-28"
	userAgent      = "komi-go/0.3"
	maxBodyBytes   = 16 << 20
)

// Error is a typed API failure the UI can map to copy without parsing strings.
type Error struct {
	Status     int
	Code       string
	Message    string
	Rate       bool
	ResetAt    time.Time
	Remaining  int
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Rate {
		reset := e.ResetAt
		if e.RetryAfter > 0 {
			reset = time.Now().Add(e.RetryAfter)
		}
		if !reset.IsZero() {
			return fmt.Sprintf("GitHub 接口额度已用完，%s 后恢复", HumanDuration(time.Until(reset)))
		}
		return "GitHub 接口额度已用完"
	}
	return e.Message
}

// CodeOf returns a stable machine code for diagnostics.
func CodeOf(err error) string {
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Code != "" {
		return apiErr.Code
	}
	return "E_UNKNOWN"
}

// IsRateLimited reports whether err is a rate-limit error.
func IsRateLimited(err error) (*Error, bool) {
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Rate {
		return apiErr, true
	}
	return nil, false
}

// IsNotFound reports a 404 without leaking the HTTP type to callers.
func IsNotFound(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

// IsUnauthorized reports an expired/revoked token.
func IsUnauthorized(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Status == 401
}

// Client talks to one GitHub-compatible host.
type Client struct {
	http    *http.Client
	base    string // API base
	web     string // website base, used for device flow
	host    string
	token   func() string
	store   *ledger.Store
	on401   func()
	sf      singleflight.Group
	retries int

	mu   sync.Mutex
	last catalog.RateLimit
	mem  map[string]memEntry
}

type memEntry struct {
	payload string
	at      time.Time
}

// New creates a github.com client. token may be nil for anonymous use.
func New(token func() string) *Client {
	hc := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return errors.New("重定向次数过多")
			}
			// Never forward the token across hosts. Go already strips
			// Authorization on cross-domain redirects; this is belt and braces.
			if len(via) > 0 && !sameSite(req.URL.Host, via[0].URL.Host) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
	return &Client{
		http: hc, base: defaultAPIBase, web: defaultWebBase, host: "github.com",
		token: token, retries: 3, mem: map[string]memEntry{},
	}
}

// NewForHost points the client at GitHub Enterprise or a test server.
func NewForHost(baseURL string, token func() string) *Client {
	c := New(token)
	c.base = strings.TrimRight(baseURL, "/")
	if u, err := url.Parse(c.base); err == nil {
		c.host = u.Host
		c.web = u.Scheme + "://" + u.Host
	}
	return c
}

// NewForgejo creates a client for a Forgejo/Codeberg/Gitea host.
func NewForgejo(host string, token func() string) *Client {
	h := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	c := New(token)
	c.host = strings.ToLower(h)
	c.web = "https://" + c.host
	c.base = c.web + "/api/v1"
	return c
}

// SetStore wires the SQLite cache. Safe to call once after construction.
func (c *Client) SetStore(store *ledger.Store) { c.store = store }

// SetAPIBase overrides the REST API base, e.g. a mirror that fronts
// api.github.com. Empty restores the official endpoint.
func (c *Client) SetAPIBase(base string) {
	if c.IsForgejo() {
		return
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultAPIBase
	}
	c.base = base
}

// SetHTTPClient swaps the transport (used when the user changes the proxy).
func (c *Client) SetHTTPClient(h *http.Client) {
	if h != nil {
		c.http = h
	}
}

// SetUnauthorizedHandler is called when the server answers 401.
func (c *Client) SetUnauthorizedHandler(fn func()) { c.on401 = fn }

// Host returns the website host this client serves.
func (c *Client) Host() string { return c.host }

// IsForgejo reports whether this client speaks the Forgejo API dialect.
func (c *Client) IsForgejo() bool {
	return c.base != defaultAPIBase && strings.HasSuffix(c.base, "/api/v1")
}

// RateLimit returns the most recent budget snapshot.
func (c *Client) RateLimit() catalog.RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// RefreshRateLimit asks api.github.com for the current anonymous/token budget.
// It deliberately goes direct instead of through a download mirror: mirrors
// often strip X-RateLimit-* headers, which is why the UI could show 0/0.
func (c *Client) RefreshRateLimit(ctx context.Context) (catalog.RateLimit, error) {
	if c.IsForgejo() {
		return c.RateLimit(), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/rate_limit", nil)
	if err != nil {
		return c.RateLimit(), err
	}
	res, err := c.execute(req, "application/vnd.github+json", "")
	if err != nil {
		return c.RateLimit(), err
	}
	if res.err != nil {
		return c.RateLimit(), res.err
	}
	return c.RateLimit(), nil
}

// SetToken updates the credential supplier at runtime.
func (c *Client) SetToken(fn func() string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = fn
}

func (c *Client) authToken() string {
	c.mu.Lock()
	fn := c.token
	c.mu.Unlock()
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn())
}

func (c *Client) recordRate(h http.Header) {
	limit, _ := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, _ := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	reset, _ := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	c.mu.Lock()
	c.last = catalog.RateLimit{
		Limit:     limit,
		Remaining: remaining,
		Reset:     time.Unix(reset, 0),
	}
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// cache + transport
// ---------------------------------------------------------------------------

// FetchMeta tells the UI whether it is looking at fresh or stale data.
type FetchMeta struct {
	Cached    bool      `json:"cached"`
	Stale     bool      `json:"stale"`
	FetchedAt time.Time `json:"fetchedAt"`
	Status    int       `json:"status"`
}

type cacheSpec struct {
	key   string
	kind  string
	fresh time.Duration
	max   time.Duration
	force bool
}

type outcome struct {
	body   []byte
	etag   string
	status int
	meta   FetchMeta
}

func (c *Client) cachedBody(ctx context.Context, path string, q url.Values, accept string, spec cacheSpec) (outcome, error) {
	key := spec.key
	if key == "" {
		key = path + "?" + q.Encode()
	}
	// Single-flight the whole cache+network path so 20 concurrent callers cost
	// one request. Followers decode the shared body into their own struct.
	v, err, _ := c.sf.Do(key, func() (any, error) {
		return c.cachedBodyOnce(ctx, path, q, accept, spec, key)
	})
	if err != nil {
		return outcome{}, err
	}
	return *v.(*outcome), nil
}

func (c *Client) cachedBodyOnce(ctx context.Context, path string, q url.Values, accept string, spec cacheSpec, key string) (*outcome, error) {
	var entry ledger.CacheEntry
	var haveEntry bool
	if c.store != nil && !spec.force {
		if e, ok, err := c.store.GetCache(ctx, key); err == nil && ok {
			entry, haveEntry = e, true
			if e.Status >= 400 && time.Now().Before(e.ExpiresAt) {
				return nil, errorFromCache(e)
			}
			if e.Status < 400 && time.Now().Before(e.ExpiresAt) {
				body := []byte(e.Payload)
				return &outcome{body: body, etag: e.ETag, status: e.Status,
					meta: FetchMeta{Cached: true, FetchedAt: e.FetchedAt, Status: e.Status}}, nil
			}
		}
	}

	etag := ""
	if haveEntry && entry.Status < 400 && !spec.force {
		etag = entry.ETag
	}

	res, err := c.fetch(ctx, path, q, accept, etag)
	if err != nil {
		// Offline / 5xx fallback to stale cache.
		if haveEntry && entry.Status < 400 && entry.Payload != "" {
			return &outcome{body: []byte(entry.Payload), etag: entry.ETag, status: entry.Status,
				meta: FetchMeta{Cached: true, Stale: true, FetchedAt: entry.FetchedAt, Status: entry.Status}}, nil
		}
		// Negative cache: a 404 is remembered for five minutes so a retry loop
		// cannot burn the API budget on a repository that does not exist.
		var apiErr *Error
		if c.store != nil && errors.As(err, &apiErr) && apiErr.Status == 404 {
			_ = c.store.PutCache(ctx, key, 404, "", spec.kind, apiErr.Message, 5*time.Minute)
		}
		return nil, err
	}

	now := time.Now()
	switch {
	case res.status == http.StatusNotModified && haveEntry:
		if c.store != nil {
			_ = c.store.TouchCache(ctx, key, spec.fresh)
		}
		return &outcome{body: []byte(entry.Payload), etag: entry.ETag, status: entry.Status,
			meta: FetchMeta{Cached: true, FetchedAt: now, Status: entry.Status}}, nil
	case res.status >= 200 && res.status < 300:
		if c.store != nil {
			_ = c.store.PutCache(ctx, key, res.status, res.etag, spec.kind, string(res.body), spec.fresh)
		}
		return &outcome{body: res.body, etag: res.etag, status: res.status,
			meta: FetchMeta{FetchedAt: now, Status: res.status}}, nil
	}
	return nil, res.err
}

type rawResponse struct {
	status     int
	body       []byte
	etag       string
	retryAfter time.Duration
	resetAt    time.Time
	err        error
}

func (c *Client) fetch(ctx context.Context, path string, q url.Values, accept, etag string) (rawResponse, error) {
	attempts := c.retries
	if attempts < 1 {
		attempts = 1
	}
	var last rawResponse
	for i := 0; i < attempts; i++ {
		if i > 0 {
			delay := time.Duration(1<<uint(i-1)) * time.Second
			delay += time.Duration(rand.Intn(400)) * time.Millisecond
			select {
			case <-ctx.Done():
				return rawResponse{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		res, err := c.fetchOnce(ctx, path, q, accept, etag)
		if err != nil {
			last = rawResponse{err: err}
			if !retryable(err) {
				return rawResponse{}, err
			}
			continue
		}
		if res.status >= 500 {
			last = res
			continue
		}
		if res.status == http.StatusTooManyRequests || res.rateLimited() {
			wait := res.retryAfter
			if wait <= 0 {
				wait = time.Until(res.resetAt)
			}
			if wait > 10*time.Second {
				return rawResponse{}, res.err
			}
			last = res
			if wait > 0 {
				select {
				case <-ctx.Done():
					return rawResponse{}, ctx.Err()
				case <-time.After(wait):
				}
			}
			continue
		}
		return res, res.err
	}
	return rawResponse{}, last.err
}

func (r rawResponse) rateLimited() bool {
	var apiErr *Error
	return errors.As(r.err, &apiErr) && apiErr.Rate
}

func retryable(err error) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 401, 403, 404, 422:
			return false
		}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return err != nil && !errors.Is(err, context.Canceled)
}

func (c *Client) fetchOnce(ctx context.Context, path string, q url.Values, accept, etag string) (rawResponse, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return rawResponse{}, err
	}
	return c.execute(req, accept, etag)
}

func (c *Client) execute(req *http.Request, accept, etag string) (rawResponse, error) {
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)
	if !c.IsForgejo() {
		req.Header.Set("X-GitHub-Api-Version", apiVersion)
	}
	if tok := c.authToken(); tok != "" {
		if c.IsForgejo() {
			req.Header.Set("Authorization", "token "+tok)
		} else {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return rawResponse{err: netError(err)}, nil
	}
	defer resp.Body.Close()
	c.recordRate(resp.Header)

	res := rawResponse{status: resp.StatusCode, etag: resp.Header.Get("ETag")}
	if resp.StatusCode == http.StatusNotModified {
		return res, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if readErr != nil {
		res.err = readErr
		return res, nil
	}
	res.body = body

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return res, nil
	case resp.StatusCode == http.StatusUnauthorized:
		if c.on401 != nil {
			go c.on401()
		}
		res.err = &Error{Status: 401, Code: "E_AUTH", Message: "登录已失效，请重新登录 GitHub"}
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		apiErr := &Error{Status: resp.StatusCode, Code: "E_RATE_LIMIT", Rate: true}
		apiErr.ResetAt = parseReset(resp.Header)
		apiErr.Remaining, _ = strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				apiErr.RetryAfter = time.Duration(secs) * time.Second
			}
		}
		if !apiErr.Rate || resp.StatusCode == http.StatusForbidden {
			apiErr.Message = messageFromBody(body)
			if apiErr.Message == "" {
				apiErr.Message = "没有访问权限（可能是私有仓库或 token 权限不足）"
			}
			if resp.StatusCode == http.StatusForbidden {
				apiErr.Code = "E_FORBIDDEN"
			}
		}
		res.err = apiErr
		res.retryAfter = apiErr.RetryAfter
		res.resetAt = apiErr.ResetAt
	case resp.StatusCode == http.StatusNotFound:
		res.err = &Error{Status: 404, Code: "E_NOT_FOUND", Message: "仓库或版本不存在，可能已被删除或转为私有"}
	default:
		res.err = &Error{Status: resp.StatusCode, Code: "E_HTTP", Message: fmt.Sprintf("GitHub 返回 %d：%s", resp.StatusCode, messageFromBody(body))}
	}
	return res, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, q url.Values, body io.Reader, out any) (rawResponse, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return rawResponse{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.execute(req, "application/vnd.github+json", "")
}

func netError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return &Error{Status: 0, Code: "E_OFFLINE", Message: "网络超时，请检查网络或代理"}
	}
	return &Error{Status: 0, Code: "E_OFFLINE", Message: "网络不可用，正在显示缓存内容", RetryAfter: 0}
}

func errorFromCache(e ledger.CacheEntry) error {
	return &Error{Status: e.Status, Code: "E_NOT_FOUND", Message: "仓库或版本不存在（缓存结果）"}
}

func parseReset(h http.Header) time.Time {
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

func messageFromBody(body []byte) string {
	var envelope struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if envelope.Message != "" {
			return envelope.Message
		}
		if len(envelope.Errors) > 0 {
			return envelope.Errors[0].Message
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// HumanDuration renders a short Chinese duration ("3 分钟").
func HumanDuration(d time.Duration) string {
	if d <= 0 {
		return "片刻"
	}
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()+0.5))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("%d 小时", int(d.Hours()+0.5))
}

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

type repoDTO struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	FullName      string    `json:"full_name"`
	Description   string    `json:"description"`
	Stars         int       `json:"stargazers_count"`
	Forks         int       `json:"forks_count"`
	OpenIssues    int       `json:"open_issues_count"`
	DefaultBranch string    `json:"default_branch"`
	Topics        []string  `json:"topics"`
	Archived      bool      `json:"archived"`
	HTMLURL       string    `json:"html_url"`
	UpdatedAt     time.Time `json:"updated_at"`
	Owner         struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	} `json:"owner"`
	License *struct {
		SPDXID string `json:"spdx_id"`
	} `json:"license"`

	// Forgejo aliases: Gitea uses stars_count instead of stargazers_count.
	StarsCount int    `json:"stars_count"`
	AvatarURL  string `json:"avatar_url"`
}

func (d repoDTO) toInfo(host string) catalog.RepoInfo {
	info := catalog.RepoInfo{
		Owner:         d.Owner.Login,
		Name:          d.Name,
		FullName:      d.FullName,
		Description:   d.Description,
		Stars:         d.Stars,
		Forks:         d.Forks,
		OpenIssues:    d.OpenIssues,
		DefaultBranch: d.DefaultBranch,
		AvatarURL:     d.Owner.AvatarURL,
		HTMLURL:       d.HTMLURL,
		Topics:        d.Topics,
		Archived:      d.Archived,
		UpdatedAt:     d.UpdatedAt,
		SourceHost:    host,
	}
	if info.Stars == 0 {
		info.Stars = d.StarsCount
	}
	if info.AvatarURL == "" {
		info.AvatarURL = d.AvatarURL
	}
	if d.License != nil {
		info.License = d.License.SPDXID
	}
	if info.HTMLURL == "" && host != "" {
		info.HTMLURL = "https://" + host + "/" + info.Owner + "/" + info.Name
	}
	return info
}

type releaseDTO struct {
	ID          int64      `json:"id"`
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt time.Time  `json:"published_at"`
	CreatedAt   time.Time  `json:"created_at"`
	HTMLURL     string     `json:"html_url"`
	Assets      []assetDTO `json:"assets"`
}

type assetDTO struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	ContentType        string `json:"content_type"`
	BrowserDownloadURL string `json:"browser_download_url"`
	URL                string `json:"url"`
	Digest             string `json:"digest"`
	DownloadCount      int64  `json:"download_count"`
}

func (d releaseDTO) toRelease() catalog.Release {
	r := catalog.Release{
		ID: d.ID, TagName: d.TagName, Name: d.Name, Body: d.Body,
		Draft: d.Draft, Prerelease: d.Prerelease,
		PublishedAt: d.PublishedAt, CreatedAt: d.CreatedAt, HTMLURL: d.HTMLURL,
	}
	for _, a := range d.Assets {
		r.Assets = append(r.Assets, catalog.Asset{
			ID: a.ID, Name: a.Name, Size: a.Size, ContentType: a.ContentType,
			DownloadURL: a.BrowserDownloadURL, APIURL: a.URL,
			Digest: a.Digest, DownloadCount: a.DownloadCount,
		})
	}
	return r
}

// ---------------------------------------------------------------------------
// API methods
// ---------------------------------------------------------------------------

// Repo fetches repository metadata (10 min fresh, 24 h offline fallback).
func (c *Client) Repo(ctx context.Context, owner, repo string) (catalog.RepoInfo, FetchMeta, error) {
	path := fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	if c.IsForgejo() {
		path = fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	}
	out, err := c.cachedBody(ctx, path, nil, "", cacheSpec{
		key: c.key("repo", owner+"/"+repo), kind: "repo",
		fresh: 10 * time.Minute, max: 24 * time.Hour,
	})
	if err != nil {
		return catalog.RepoInfo{}, out.meta, err
	}
	var dto repoDTO
	if err := json.Unmarshal(out.body, &dto); err != nil {
		return catalog.RepoInfo{}, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "仓库数据格式异常"}
	}
	return dto.toInfo(c.host), out.meta, nil
}

// RepoDetail bypasses the cache (manual refresh).
func (c *Client) RepoDetail(ctx context.Context, owner, repo string) (catalog.RepoInfo, FetchMeta, error) {
	out, err := c.cachedBody(ctx, fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo)), nil, "",
		cacheSpec{key: c.key("repo", owner+"/"+repo), kind: "repo", fresh: 10 * time.Minute, max: 24 * time.Hour, force: true})
	if err != nil {
		return catalog.RepoInfo{}, out.meta, err
	}
	var dto repoDTO
	if err := json.Unmarshal(out.body, &dto); err != nil {
		return catalog.RepoInfo{}, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "仓库数据格式异常"}
	}
	return dto.toInfo(c.host), out.meta, nil
}

// Releases returns one page of releases. force bypasses the fresh cache while
// still keeping the stale copy for offline fallback.
func (c *Client) Releases(ctx context.Context, owner, repo string, perPage int, force bool) ([]catalog.Release, FetchMeta, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 30
	}
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(perPage))
	path := fmt.Sprintf("/repos/%s/%s/releases", url.PathEscape(owner), url.PathEscape(repo))
	if c.IsForgejo() {
		q.Del("per_page")
		q.Set("limit", strconv.Itoa(perPage))
	}
	out, err := c.cachedBody(ctx, path, q, "", cacheSpec{
		key: c.key("releases", fmt.Sprintf("%s/%s:%d", owner, repo, perPage)), kind: "releases",
		fresh: 5 * time.Minute, max: 2 * time.Hour, force: force,
	})
	if err != nil {
		return nil, out.meta, err
	}
	var dtos []releaseDTO
	if err := json.Unmarshal(out.body, &dtos); err != nil {
		return nil, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "Release 数据格式异常"}
	}
	releases := make([]catalog.Release, 0, len(dtos))
	for _, d := range dtos {
		releases = append(releases, d.toRelease())
	}
	return releases, out.meta, nil
}

// Readme returns the repository README as raw Markdown (6 h fresh, 7 d stale).
func (c *Client) Readme(ctx context.Context, owner, repo string) (string, FetchMeta, error) {
	path := fmt.Sprintf("/repos/%s/%s/readme", url.PathEscape(owner), url.PathEscape(repo))
	if c.IsForgejo() {
		return c.forgejoReadme(ctx, owner, repo)
	}
	out, err := c.cachedBody(ctx, path, nil, "application/vnd.github.raw+json", cacheSpec{
		key: c.key("readme", owner+"/"+repo), kind: "readme",
		fresh: 6 * time.Hour, max: 7 * 24 * time.Hour,
	})
	if err != nil {
		return "", out.meta, err
	}
	return string(out.body), out.meta, nil
}

func (c *Client) forgejoReadme(ctx context.Context, owner, repo string) (string, FetchMeta, error) {
	path := fmt.Sprintf("/repos/%s/%s/contents/README.md", url.PathEscape(owner), url.PathEscape(repo))
	out, err := c.cachedBody(ctx, path, nil, "", cacheSpec{
		key: c.key("readme", owner+"/"+repo), kind: "readme",
		fresh: 6 * time.Hour, max: 7 * 24 * time.Hour,
	})
	if err != nil {
		return "", out.meta, err
	}
	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(out.body, &payload); err != nil || payload.Content == "" {
		return "", out.meta, &Error{Status: 404, Code: "E_NOT_FOUND", Message: "这个仓库没有 README"}
	}
	raw := strings.ReplaceAll(payload.Content, "\n", "")
	if payload.Encoding == "base64" {
		if decoded, err := decodeBase64(raw); err == nil {
			return string(decoded), out.meta, nil
		}
	}
	return raw, out.meta, nil
}

// Search queries repositories. sort is one of stars|forks|updated|"".
func (c *Client) Search(ctx context.Context, query string, page, perPage int, sort, language string, force bool) ([]catalog.RepoInfo, FetchMeta, error) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 || perPage > 100 {
		perPage = 24
	}
	lang := strings.TrimSpace(language)
	if lang != "" && !strings.Contains(query, "language:") {
		query = strings.TrimSpace(query + " language:" + lang)
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))
	switch sort {
	case "stars", "forks", "updated":
		q.Set("sort", sort)
		q.Set("order", "desc")
	}
	path := "/search/repositories"
	if c.IsForgejo() {
		path = "/repos/search"
		q.Del("q")
		q.Set("q", query)
		q.Del("per_page")
		q.Del("sort")
		q.Set("limit", strconv.Itoa(perPage))
	}
	out, err := c.cachedBody(ctx, path, q, "", cacheSpec{
		key: c.key("search", fmt.Sprintf("%s:%d:%d:%s:%s", query, page, perPage, sort, lang)), kind: "search",
		fresh: 2 * time.Minute, max: 30 * time.Minute, force: force,
	})
	if err != nil {
		return nil, out.meta, err
	}
	var envelope struct {
		Items []repoDTO `json:"items"`
		Data  []repoDTO `json:"data"`
	}
	if err := json.Unmarshal(out.body, &envelope); err != nil {
		return nil, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "搜索数据格式异常"}
	}
	items := envelope.Items
	if len(items) == 0 {
		items = envelope.Data
	}
	repos := make([]catalog.RepoInfo, 0, len(items))
	for _, d := range items {
		repos = append(repos, d.toInfo(c.host))
	}
	return repos, out.meta, nil
}

// Login validates the token and returns the account name. It uses the retrying
// transport so a transient 5xx or a 429 with Retry-After does not surface as a
// failed sign-in.
func (c *Client) Login(ctx context.Context) (string, error) {
	res, err := c.fetch(ctx, "/user", nil, "", "")
	if err != nil {
		return "", err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(res.body, &user); err != nil || user.Login == "" {
		return "", &Error{Status: 500, Code: "E_PARSE", Message: "无法解析账号信息"}
	}
	return user.Login, nil
}

func (c *Client) key(kind, suffix string) string {
	return kind + ":" + c.host + ":" + suffix
}

func (c *Client) keyHash(prefix, raw string) string {
	sum := sha256.Sum256([]byte(c.host + "|" + raw))
	return prefix + ":" + c.host + ":" + hex.EncodeToString(sum[:8])
}

func sameSite(a, b string) bool {
	ha := strings.ToLower(a)
	hb := strings.ToLower(b)
	if ha == hb {
		return true
	}
	return strings.HasSuffix(ha, "."+hb) || strings.HasSuffix(hb, "."+ha)
}

func decodeBase64(s string) ([]byte, error) {
	// Forgejo may return wrapped base64 with newlines; strip before decoding.
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' {
			return -1
		}
		return r
	}, s)
	return base64.StdEncoding.DecodeString(s)
}

func buildJSON(v any) (io.Reader, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}
