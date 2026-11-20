// Package github: this file talks to the Komi aggregation backend
// (https://api.github-store.org/v1) that the upstream desktop app uses.
//
// Why it exists: api.github.com anonymous requests are limited to 60/hour and
// 10 search requests/minute. The backend keeps a shared pool of service tokens,
// a release index and category feeds, so a fresh install can show a populated
// store without asking the user to sign in. Direct GitHub calls remain as the
// fallback for private repos, Forgejo and backend outages.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	backendDefaultBase = "https://api.github-store.org/v1"
	backendUserAgent   = "komi-go/0.3"
	backendPageSize    = 20
	backendFeedMirror  = "https://raw.githubusercontent.com/komi-store/komi-store-backend-data/main/cached-data/feed/windows.json"
)

// Backend is a read-only client for the Komi aggregation service.
type Backend struct {
	http  *http.Client
	base  string
	token func() string
	store *ledger.Store
	sf    singleflight.Group

	mu   sync.Mutex
	last catalog.RateLimit
}

// NewBackend creates the public backend client. token may be nil.
func NewBackend(token func() string) *Backend {
	return &Backend{
		http:  &http.Client{Timeout: 30 * time.Second},
		base:  backendDefaultBase,
		token: token,
	}
}

func (b *Backend) SetStore(store *ledger.Store) { b.store = store }

func (b *Backend) SetHTTPClient(h *http.Client) {
	if h != nil {
		b.http = h
	}
}

func (b *Backend) SetToken(fn func() string) {
	b.mu.Lock()
	b.token = fn
	b.mu.Unlock()
}

func (b *Backend) authToken() string {
	b.mu.Lock()
	fn := b.token
	b.mu.Unlock()
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn())
}

// RateLimit is the most recent backend budget snapshot.
func (b *Backend) RateLimit() catalog.RateLimit {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

// BackendShouldFallback reports whether a backend failure is recoverable by
// talking to api.github.com directly. 429 and 401 are deliberately not
// recoverable: falling back would multiply the pressure that caused them.
func BackendShouldFallback(err error) bool {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return true
	}
	switch {
	case apiErr.Status == 0:
		return true
	case apiErr.Status == 404:
		return true
	case apiErr.Status >= 500:
		return true
	}
	return false
}

// flexStrings accepts both the API's string arrays and the offline mirror's
// space-separated strings for topics/topicCodes.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*f = nil
		return nil
	}
	switch data[0] {
	case '[':
		var list []string
		if err := json.Unmarshal(data, &list); err != nil {
			return err
		}
		*f = list
		return nil
	case '"':
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		*f = strings.Fields(raw)
		return nil
	default:
		return fmt.Errorf("invalid string list: %s", data)
	}
}

// backendRepo is the JSON shape used by categories/feed/search/repo.
type backendRepo struct {
	ID               int64                             `json:"id"`
	Name             string                            `json:"name"`
	FullName         string                            `json:"fullName"`
	Description      string                            `json:"description"`
	DefaultBranch    string                            `json:"defaultBranch"`
	HTMLURL          string                            `json:"htmlUrl"`
	Stars            int                               `json:"stargazersCount"`
	Forks            int                               `json:"forksCount"`
	OpenIssues       int                               `json:"openIssuesCount"`
	Language         string                            `json:"language"`
	Topics           flexStrings                       `json:"topics"`
	TopicCodes       flexStrings                       `json:"topicCodes"`
	LicenseSPDX      string                            `json:"licenseSpdxId"`
	LicenseName      string                            `json:"licenseName"`
	UpdatedAt        string                            `json:"updatedAt"`
	PushedAt         string                            `json:"pushedAt"`
	CreatedAt        string                            `json:"createdAt"`
	LatestReleaseAt  string                            `json:"latestReleaseDate"`
	LatestReleaseTag string                            `json:"latestReleaseTag"`
	DownloadCount    int64                             `json:"downloadCount"`
	DailyStars       int                               `json:"dailyStars"`
	TrendingScore    float64                           `json:"trendingScore"`
	PopularityScore  float64                           `json:"popularityScore"`
	HasWin           bool                              `json:"hasInstallersWindows"`
	HasMac           bool                              `json:"hasInstallersMacos"`
	HasLinux         bool                              `json:"hasInstallersLinux"`
	HasAndroid       bool                              `json:"hasInstallersAndroid"`
	PlatformReleases map[string]backendPlatformRelease `json:"platformReleases"`
	SourceHost       string                            `json:"source_host"`
	Owner            struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatarUrl"`
	} `json:"owner"`
	License *struct {
		SPDXID string `json:"spdxId"`
		Name   string `json:"name"`
	} `json:"license"`
}

type backendPlatformRelease struct {
	Tag         string `json:"tag"`
	PublishedAt string `json:"publishedAt"`
}

func (d backendRepo) toInfo() catalog.RepoInfo {
	owner := d.Owner.Login
	full := d.FullName
	if full == "" && owner != "" {
		full = owner + "/" + d.Name
	}
	info := catalog.RepoInfo{
		Owner:            owner,
		Name:             d.Name,
		FullName:         full,
		Description:      d.Description,
		Stars:            d.Stars,
		Forks:            d.Forks,
		OpenIssues:       d.OpenIssues,
		DefaultBranch:    d.DefaultBranch,
		AvatarURL:        d.Owner.AvatarURL,
		HTMLURL:          d.HTMLURL,
		Topics:           []string(d.Topics),
		UpdatedAt:        parseBackendTime(d.UpdatedAt),
		SourceHost:       "github.com",
		Language:         d.Language,
		LatestReleaseTag: d.LatestReleaseTag,
		LatestReleaseAt:  parseBackendTime(d.LatestReleaseAt),
		DownloadCount:    d.DownloadCount,
		DailyStars:       d.DailyStars,
		TrendingScore:    d.TrendingScore,
		PopularityScore:  d.PopularityScore,
		TopicCodes:       []string(d.TopicCodes),
	}
	if info.LatestReleaseAt.IsZero() {
		if pr, ok := d.PlatformReleases["windows"]; ok {
			info.LatestReleaseAt = parseBackendTime(pr.PublishedAt)
			if info.LatestReleaseTag == "" {
				info.LatestReleaseTag = pr.Tag
			}
		}
	}
	info.License = d.LicenseSPDX
	if d.License != nil && d.License.SPDXID != "" {
		info.License = d.License.SPDXID
	}
	if info.HTMLURL == "" && full != "" {
		info.HTMLURL = "https://github.com/" + full
	}
	info.PlatformReady = d.HasWin
	if info.PlatformReady {
		info.Platforms = append(info.Platforms, catalog.PlatformWindows)
	}
	if d.HasMac {
		info.Platforms = append(info.Platforms, catalog.PlatformMacOS)
	}
	if d.HasLinux {
		info.Platforms = append(info.Platforms, catalog.PlatformLinux)
	}
	if d.HasAndroid {
		info.Platforms = append(info.Platforms, catalog.PlatformAndroid)
	}
	if !d.HasWin {
		info.PlatformNote = "没有 Windows 安装包"
	}
	if len(info.Platforms) == 0 {
		info.PlatformNote = "仅提供源码"
	}
	return info
}

func parseBackendTime(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// transport + cache
// ---------------------------------------------------------------------------

func (b *Backend) cacheKey(path string, q url.Values) string {
	auth := "anon"
	if b.authToken() != "" {
		auth = "auth"
	}
	return "backend:" + auth + ":" + path + "?" + q.Encode()
}

func (b *Backend) get(ctx context.Context, path string, q url.Values, spec cacheSpec) (outcome, error) {
	if spec.key == "" {
		spec.key = b.cacheKey(path, q)
	}
	v, err, _ := b.sf.Do(spec.key, func() (any, error) {
		return b.getOnce(ctx, path, q, spec)
	})
	if err != nil {
		return outcome{}, err
	}
	return *v.(*outcome), nil
}

func (b *Backend) getOnce(ctx context.Context, path string, q url.Values, spec cacheSpec) (*outcome, error) {
	var entry ledger.CacheEntry
	haveEntry := false
	if b.store != nil && !spec.force {
		if e, ok, err := b.store.GetCache(ctx, spec.key); err == nil && ok {
			entry, haveEntry = e, true
			if e.Status < 400 && time.Now().Before(e.ExpiresAt) {
				return &outcome{
					body: []byte(e.Payload), etag: e.ETag, status: e.Status,
					meta: FetchMeta{Cached: true, FetchedAt: e.FetchedAt, Status: e.Status},
				}, nil
			}
		}
	}
	etag := ""
	if haveEntry && entry.Status < 400 && !spec.force {
		etag = entry.ETag
	}

	res, err := b.fetch(ctx, path, q, etag)
	if err != nil {
		if haveEntry && entry.Status < 400 && entry.Payload != "" {
			return &outcome{
				body: []byte(entry.Payload), etag: entry.ETag, status: entry.Status,
				meta: FetchMeta{Cached: true, Stale: true, FetchedAt: entry.FetchedAt, Status: entry.Status},
			}, nil
		}
		return nil, err
	}

	now := time.Now()
	if res.status == http.StatusNotModified && haveEntry {
		if b.store != nil {
			_ = b.store.TouchCache(ctx, spec.key, spec.fresh)
		}
		return &outcome{
			body: []byte(entry.Payload), etag: entry.ETag, status: entry.Status,
			meta: FetchMeta{Cached: true, FetchedAt: now, Status: entry.Status},
		}, nil
	}
	if b.store != nil && res.status < 400 {
		_ = b.store.PutCache(ctx, spec.key, res.status, res.etag, "backend", string(res.body), spec.fresh)
	}
	return &outcome{
		body: res.body, etag: res.etag, status: res.status,
		meta: FetchMeta{FetchedAt: now, Status: res.status},
	}, nil
}

func (b *Backend) fetch(ctx context.Context, path string, q url.Values, etag string) (rawResponse, error) {
	var last rawResponse
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return rawResponse{}, ctx.Err()
			case <-time.After(1200 * time.Millisecond):
			}
		}
		res, err := b.fetchOnce(ctx, path, q, etag)
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
		if res.status == http.StatusTooManyRequests {
			return rawResponse{}, res.err
		}
		return res, res.err
	}
	if last.err != nil {
		return rawResponse{}, last.err
	}
	if last.body != nil {
		return last, nil
	}
	return rawResponse{}, &Error{Status: 0, Code: "E_BACKEND", Message: "聚合服务暂时不可用"}
}

func (b *Backend) fetchOnce(ctx context.Context, path string, q url.Values, etag string) (rawResponse, error) {
	base := strings.TrimRight(b.base, "/")
	u := path
	external := strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
	if !external {
		u = base + "/" + strings.TrimLeft(path, "/")
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return rawResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", backendUserAgent)
	if tok := b.authToken(); tok != "" && !external {
		req.Header.Set("X-GitHub-Token", tok)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := b.http.Do(req)
	if err != nil {
		return rawResponse{err: netError(err)}, nil
	}
	defer resp.Body.Close()

	limit, _ := strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
	remaining, _ := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	reset, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	b.mu.Lock()
	b.last = catalog.RateLimit{Limit: limit, Remaining: remaining, Reset: time.Unix(reset, 0)}
	b.mu.Unlock()

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
	case resp.StatusCode == http.StatusTooManyRequests:
		apiErr := &Error{Status: 429, Code: "E_BACKEND_RATE", Rate: true, Message: "Komi 聚合服务请求过多，请稍后再试"}
		apiErr.ResetAt = parseReset(resp.Header)
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				apiErr.RetryAfter = time.Duration(secs) * time.Second
			}
		}
		res.err = apiErr
	case resp.StatusCode == http.StatusNotFound:
		res.err = &Error{Status: 404, Code: "E_NOT_FOUND", Message: "聚合服务没有收录这个仓库"}
	case resp.StatusCode >= 500:
		res.err = &Error{Status: resp.StatusCode, Code: "E_BACKEND", Message: "Komi 聚合服务暂时不可用"}
	case resp.StatusCode == http.StatusBadRequest:
		res.err = &Error{Status: 400, Code: "E_BACKEND_REQUEST", Message: "聚合服务不接受这个查询"}
	default:
		res.err = &Error{Status: resp.StatusCode, Code: "E_BACKEND", Message: fmt.Sprintf("聚合服务返回 %d", resp.StatusCode)}
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// public read methods
// ---------------------------------------------------------------------------

// Discover returns the upstream curated categories. kind is
// trending|fresh|classic|windows. Category lists are 50 entries, sliced into
// 20-item pages locally so the existing pager keeps working.
func (b *Backend) Discover(ctx context.Context, kind string, page int, onlyInstallable bool) ([]catalog.RepoInfo, FetchMeta, error) {
	if page <= 0 {
		page = 1
	}
	var path string
	var spec cacheSpec
	switch kind {
	case "fresh":
		path = "categories/new-releases/windows"
		spec = cacheSpec{kind: "backend-feed", fresh: 5 * time.Minute, max: 24 * time.Hour}
	case "classic":
		path = "categories/most-popular/windows"
		spec = cacheSpec{kind: "backend-feed", fresh: 10 * time.Minute, max: 24 * time.Hour}
	default:
		path = "categories/trending/windows"
		spec = cacheSpec{kind: "backend-feed", fresh: 5 * time.Minute, max: 24 * time.Hour}
	}
	if kind == "windows" {
		q := url.Values{}
		q.Set("platform", "windows")
		q.Set("page", strconv.Itoa(page))
		q.Set("limit", strconv.Itoa(backendPageSize))
		out, err := b.get(ctx, "feed", q, cacheSpec{kind: "backend-feed", fresh: 5 * time.Minute, max: 24 * time.Hour})
		if err != nil {
			return nil, out.meta, err
		}
		var feed struct {
			Items   []backendRepo `json:"items"`
			Page    int           `json:"page"`
			HasMore bool          `json:"hasMore"`
		}
		if err := json.Unmarshal(out.body, &feed); err != nil {
			return nil, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "聚合服务发现数据格式异常"}
		}
		return filterBackendRepos(feed.Items, onlyInstallable), out.meta, nil
	}

	// Category endpoints ignore paging; cache the whole 50 once and slice.
	pageSpec := spec
	pageSpec.key = "backend:category:" + path
	pageSpec.force = false
	out, err := b.get(ctx, path, nil, pageSpec)
	if err != nil {
		return nil, out.meta, err
	}
	var list []backendRepo
	if err := json.Unmarshal(out.body, &list); err != nil {
		return nil, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "聚合服务分类数据格式异常"}
	}
	repos := filterBackendRepos(list, onlyInstallable)
	start := (page - 1) * backendPageSize
	if start >= len(repos) {
		return []catalog.RepoInfo{}, out.meta, nil
	}
	end := start + backendPageSize
	if end > len(repos) {
		end = len(repos)
	}
	return repos[start:end], out.meta, nil
}

// CategoryFeed returns the complete Windows Explore feed used by the
// upstream sidebar categories. Backend /feed is paged, so the upstream offline
// mirror (the same 150-repo snapshot) is read once and cached for local
// topicCodes filtering. This keeps privacy/media/tool populated even when only
// the first feed page is visible.
func (b *Backend) CategoryFeed(ctx context.Context) ([]catalog.RepoInfo, error) {
	spec := cacheSpec{
		key:   "backend:feed-catalog:windows",
		fresh: 6 * time.Hour,
		max:   7 * 24 * time.Hour,
	}
	out, err := b.get(ctx, backendFeedMirror, nil, spec)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Repositories []backendRepo `json:"repositories"`
	}
	if err := json.Unmarshal(out.body, &doc); err != nil {
		return nil, &Error{Status: 500, Code: "E_PARSE", Message: "分类数据格式异常"}
	}
	return filterBackendRepos(doc.Repositories, false), nil
}

func filterBackendRepos(list []backendRepo, only bool) []catalog.RepoInfo {
	out := make([]catalog.RepoInfo, 0, len(list))
	for _, r := range list {
		info := r.toInfo()
		if len(info.Platforms) == 0 {
			continue // source-only repositories are not installable apps
		}
		if only && !r.HasWin {
			continue
		}
		out = append(out, info)
	}
	return out
}

// Search calls /v1/search. sort is stars|updated|relevance|releases.
// platform may be empty to search every indexed platform (the app does this
// when the "installable only" filter is off, so similar apps are not hidden).
func (b *Backend) Search(ctx context.Context, query string, page, perPage int, sort, platform string, onlyInstallable bool) ([]catalog.RepoInfo, int, FetchMeta, error) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 || perPage > 50 {
		perPage = backendPageSize
	}
	q := url.Values{}
	q.Set("q", query)
	if platform != "" {
		q.Set("platform", platform)
	}
	q.Set("limit", strconv.Itoa(perPage))
	q.Set("offset", strconv.Itoa((page-1)*perPage))
	if sort != "" {
		q.Set("sort", sort)
	}
	out, err := b.get(ctx, "search", q, cacheSpec{kind: "backend-search", fresh: 2 * time.Minute, max: 30 * time.Minute})
	if err != nil {
		return nil, 0, out.meta, err
	}
	var envelope struct {
		Items     []backendRepo `json:"items"`
		TotalHits int           `json:"totalHits"`
		Source    string        `json:"source"`
	}
	if err := json.Unmarshal(out.body, &envelope); err != nil {
		return nil, 0, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "聚合服务搜索数据格式异常"}
	}
	repos := filterBackendRepos(envelope.Items, onlyInstallable)
	return repos, envelope.TotalHits, out.meta, nil
}

// Repo resolves repository metadata, including the Windows installer flag.
func (b *Backend) Repo(ctx context.Context, owner, repo string) (catalog.RepoInfo, FetchMeta, error) {
	path := "repo/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	out, err := b.get(ctx, path, nil, cacheSpec{kind: "backend-repo", fresh: 10 * time.Minute, max: 24 * time.Hour})
	if err != nil {
		return catalog.RepoInfo{}, out.meta, err
	}
	var dto backendRepo
	if err := json.Unmarshal(out.body, &dto); err != nil {
		return catalog.RepoInfo{}, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "聚合服务仓库数据格式异常"}
	}
	return dto.toInfo(), out.meta, nil
}

// Releases proxies the indexed GitHub release payload.
func (b *Backend) Releases(ctx context.Context, owner, repo string, perPage int, force bool) ([]catalog.Release, FetchMeta, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 30
	}
	q := url.Values{}
	q.Set("page", "1")
	q.Set("per_page", strconv.Itoa(perPage))
	path := "releases/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	out, err := b.get(ctx, path, q, cacheSpec{kind: "backend-releases", fresh: 5 * time.Minute, max: 2 * time.Hour, force: force})
	if err != nil {
		return nil, out.meta, err
	}
	var dtos []releaseDTO
	if err := json.Unmarshal(out.body, &dtos); err != nil {
		return nil, out.meta, &Error{Status: 500, Code: "E_PARSE", Message: "聚合服务 Release 数据格式异常"}
	}
	releases := make([]catalog.Release, 0, len(dtos))
	for _, d := range dtos {
		releases = append(releases, d.toRelease())
	}
	return releases, out.meta, nil
}

// Readme returns decoded Markdown. The backend wraps the GitHub contents API,
// so the body is base64 inside JSON rather than raw Markdown.
func (b *Backend) Readme(ctx context.Context, owner, repo string) (string, FetchMeta, error) {
	path := "readme/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	out, err := b.get(ctx, path, nil, cacheSpec{kind: "backend-readme", fresh: 6 * time.Hour, max: 7 * 24 * time.Hour})
	if err != nil {
		return "", out.meta, err
	}
	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(out.body, &payload); err != nil {
		// Some deployments may proxy raw Markdown.
		return string(out.body), out.meta, nil
	}
	if payload.Message != "" && payload.Content == "" {
		return "", out.meta, &Error{Status: 404, Code: "E_NOT_FOUND", Message: "这个仓库没有 README"}
	}
	if payload.Content == "" {
		return "", out.meta, &Error{Status: 404, Code: "E_NOT_FOUND", Message: "这个仓库没有 README"}
	}
	if payload.Encoding == "base64" {
		if decoded, err := decodeBase64(payload.Content); err == nil {
			return string(decoded), out.meta, nil
		}
	}
	return payload.Content, out.meta, nil
}
