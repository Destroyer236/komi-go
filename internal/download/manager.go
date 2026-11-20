// Package download implements the resumable downloader: .part + Range resume,
// SHA-256 verification, disk-space preflight, mirror racing, user cancel,
// queue restore after a restart and a stall watchdog that actually fires while
// a read is blocked.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"komi-go/internal/catalog"
	"komi-go/internal/ledger"
	"komi-go/internal/platform"
)

// Mirror is a user-supplied URL template. {url} is replaced with the original
// https asset URL.
type Mirror struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	API      bool   `json:"api,omitempty"`
}

// MirrorResult is one latency probe result shown in Settings.
type MirrorResult struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Latency  int64  `json:"latencyMs"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// MirrorProbeURL is a small public asset used by the Settings speed test.
const MirrorProbeURL = "https://github.com/cli/cli/releases/download/v2.83.2/gh_2.83.2_windows_amd64.zip"

// PresetMirrors are public GitHub release accelerators. They are only
// transport proxies: the asset URL is embedded unchanged in the template.
func PresetMirrors() []Mirror {
	return []Mirror{
		{Name: "NxNow", Template: "https://gh.nxnow.top/{url}"},
		{Name: "MonkeyRay", Template: "https://ghproxy.monkeyray.net/{url}", API: true},
		{Name: "GeekerTao", Template: "https://ghfile.geekertao.top/{url}"},
		{Name: "GHProxy.net", Template: "https://ghproxy.net/{url}"},
		{Name: "GHFast", Template: "https://ghfast.top/{url}"},
		{Name: "LLKK", Template: "https://gh.llkk.cc/{url}"},
	}
}

// ApplyMirror wraps a GitHub URL with a mirror template. Non-HTTPS or
// template-less mirrors are ignored.
func ApplyMirror(template, original string) string {
	template = strings.TrimSpace(template)
	if template == "" || !strings.Contains(template, "{url}") {
		return original
	}
	return strings.ReplaceAll(template, "{url}", original)
}

// Request describes one asset to fetch.
type Request struct {
	RepoKey     string        `json:"repoKey"`
	Asset       catalog.Asset `json:"asset"`
	DisplayName string        `json:"displayName"`
	Tag         string        `json:"tag"`
	PublishedAt int64         `json:"publishedAt"`
}

// Job is the JSON shape the UI consumes.
type Job struct {
	ID          string `json:"id"`
	RepoKey     string `json:"repoKey"`
	DisplayName string `json:"displayName"`
	AssetName   string `json:"assetName"`
	AssetID     int64  `json:"assetId"`
	AssetURL    string `json:"assetUrl"`
	Digest      string `json:"digest"`
	Tag         string `json:"tag"`
	PublishedAt int64  `json:"publishedAt"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	DestPath    string `json:"destPath"`
	Total       int64  `json:"total"`
	Done        int64  `json:"done"`
	Percent     int    `json:"percent"`
	Version     string `json:"version"`
	Verified    bool   `json:"verified"`
	Source      string `json:"source"`
	CanResume   bool   `json:"canResume"`
}

// Emitter forwards events to Wails.
type Emitter func(name string, payload any)

type jobState struct {
	mu     sync.Mutex
	info   Job
	req    Request
	cancel context.CancelFunc
	reason string // pause | cancel | shutdown
	done   chan struct{}
}

// Manager owns the queue and the HTTP client.
type Manager struct {
	dir    string
	client *http.Client
	ledger *ledger.Store
	token  func() string
	emit   Emitter
	sem    chan struct{}
	wg     sync.WaitGroup

	mu       sync.Mutex
	jobs     map[string]*jobState
	mirrors  []Mirror
	auto     bool
	selected string
	probes   map[string]time.Time // template -> measured latency (0 = failed)
	closed   bool
}

// New creates a manager downloading into dir.
func New(dir string, store *ledger.Store, token func() string, emit Emitter) *Manager {
	if emit == nil {
		emit = func(string, any) {}
	}
	return &Manager{
		dir:    dir,
		ledger: store,
		token:  token,
		emit:   emit,
		sem:    make(chan struct{}, 2),
		jobs:   map[string]*jobState{},
		probes: map[string]time.Time{},
		auto:   true,
		client: &http.Client{Timeout: 0},
	}
}

// SetHTTPClient swaps the transport used for transfers and mirror probes.
func (m *Manager) SetHTTPClient(h *http.Client) {
	if h != nil {
		m.client = h
	}
}

// SetAuto enables latency-based source selection. When false, the direct
// GitHub URL is always tried first, then mirrors in configured order.
func (m *Manager) SetAuto(on bool) {
	m.mu.Lock()
	m.auto = on
	m.mu.Unlock()
}

// SetSelected pins one mirror template. An empty value means "no pinned
// mirror": either auto or direct-only, depending on SetAuto.
func (m *Manager) SetSelected(template string) {
	m.mu.Lock()
	m.selected = strings.TrimSpace(template)
	m.mu.Unlock()
}

// ActiveMirror returns the pinned template, or the first API-capable preset in
// auto mode. Used for README/page resource rewriting.
func (m *Manager) ActiveMirror() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.selected != "" {
		return m.selected
	}
	if !m.auto {
		return ""
	}
	for _, mir := range m.mirrors {
		if mir.API {
			return mir.Template
		}
	}
	if len(m.mirrors) > 0 {
		return m.mirrors[0].Template
	}
	return ""
}

// SetMirrors replaces the mirror list. Duplicate/empty entries are ignored.
func (m *Manager) SetMirrors(list []Mirror) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mirrors = m.mirrors[:0]
	for _, mir := range list {
		mir.Name = strings.TrimSpace(mir.Name)
		mir.Template = strings.TrimSpace(mir.Template)
		if mir.Template == "" || !strings.HasPrefix(strings.ToLower(mir.Template), "https://") {
			continue
		}
		if !strings.Contains(mir.Template, "{url}") {
			continue
		}
		m.mirrors = append(m.mirrors, mir)
	}
	m.probes = map[string]time.Time{}
}

// Start enqueues a download and returns its initial job snapshot.
func (m *Manager) Start(req Request) (Job, error) {
	if err := platform.ValidDownloadURL(req.Asset.DownloadURL); err != nil {
		return Job{}, err
	}
	if strings.TrimSpace(req.Asset.Name) == "" {
		return Job{}, errors.New("资产名为空")
	}
	if req.Asset.Size > 0 {
		if free, err := diskFree(m.dir); err == nil && free < uint64(req.Asset.Size*3) {
			return Job{}, fmt.Errorf("磁盘空间不足：需要至少 %s，当前可用 %s",
				platform.HumanBytes(req.Asset.Size*3), platform.HumanBytes(int64(free)))
		}
	}
	name := platform.SanitizeFileName(req.Asset.Name)
	dest := platform.UniquePath(m.dir, name)
	id := jobID(req.RepoKey, req.Asset.ID, req.Asset.DownloadURL)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Job{}, errors.New("下载器已关闭")
	}
	if existing, ok := m.jobs[id]; ok {
		existing.mu.Lock()
		st := existing.info.Status
		info := existing.info
		existing.mu.Unlock()
		switch st {
		case ledger.StatusQueued, ledger.StatusDownloading, ledger.StatusPaused, ledger.StatusVerifying:
			m.mu.Unlock()
			return info, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &jobState{
		info: Job{
			ID: id, RepoKey: req.RepoKey, DisplayName: req.DisplayName,
			AssetName: req.Asset.Name, AssetID: req.Asset.ID, AssetURL: req.Asset.DownloadURL,
			Digest: req.Asset.Digest,
			Tag:    req.Tag, PublishedAt: req.PublishedAt, Status: ledger.StatusQueued,
			DestPath: dest, Version: req.Tag, Total: req.Asset.Size,
		},
		req: req, cancel: cancel, done: make(chan struct{}),
	}
	m.jobs[id] = st
	m.mu.Unlock()

	m.persist(st, ledger.StatusQueued)
	m.emit("download:added", snapshot(st))
	m.launch(ctx, st)
	return snapshot(st), nil
}

func (m *Manager) launch(ctx context.Context, st *jobState) {
	st.mu.Lock()
	done := st.done
	st.mu.Unlock()
	if done == nil {
		done = make(chan struct{})
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(done)
		m.run(ctx, st)
	}()
}

func (m *Manager) run(ctx context.Context, st *jobState) {
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		m.finishWithoutWork(st)
		return
	}
	defer func() { <-m.sem }()

	m.updateStatus(st, ledger.StatusDownloading, "")
	dest, verified, source, err := m.fetch(ctx, st)
	if err != nil {
		if reason := st.reasonOf(); reason == "cancel" {
			return
		}
		if errors.Is(err, context.Canceled) || st.reasonOf() != "" {
			m.updateStatus(st, ledger.StatusPaused, "已暂停，可继续下载")
			return
		}
		m.updateStatus(st, ledger.StatusFailed, friendly(err))
		return
	}

	st.mu.Lock()
	st.info.DestPath = dest
	st.info.Status = ledger.StatusDone
	st.info.Done = st.info.Total
	st.info.Percent = 100
	st.info.Verified = verified
	if source != "" {
		st.info.Source = source
	}
	snapshot := st.info
	verifiedFlag := verified
	st.mu.Unlock()
	m.emit("download:done", snapshot)
	if m.ledger != nil {
		_ = m.ledger.UpdateTaskProgress(context.Background(), snapshot.ID, snapshot.Done, snapshot.Total, ledger.StatusDone, "")
		_ = m.ledger.SetTaskVerified(context.Background(), snapshot.ID, verifiedFlag)
	}
}

func (m *Manager) finishWithoutWork(st *jobState) {
	if st.reasonOf() == "cancel" {
		return
	}
	m.updateStatus(st, ledger.StatusPaused, "已暂停，可继续下载")
}

// ---------------------------------------------------------------------------
// fetch
// ---------------------------------------------------------------------------

func (m *Manager) fetch(ctx context.Context, st *jobState) (string, bool, string, error) {
	req := st.req
	sources := m.sources(req.Asset.DownloadURL)
	var lastErr error
	for _, src := range sources {
		if st.reasonOf() != "" {
			return "", false, "", context.Canceled
		}
		dest, verified, err := m.fetchOne(ctx, st, req, src.url)
		if err == nil {
			return dest, verified, src.label, nil
		}
		if errors.Is(err, context.Canceled) || st.reasonOf() != "" {
			return "", false, "", context.Canceled
		}
		lastErr = err
		// A 404/410 means the asset itself is gone; mirrors cannot help.
		if strings.Contains(err.Error(), "资产已不存在") {
			return "", false, "", err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的下载源")
	}
	return "", false, "", lastErr
}

type source struct {
	label string
	url   string
}

func (m *Manager) sources(original string) []source {
	direct := source{label: "GitHub 直连", url: original}
	m.mu.Lock()
	mirrors := append([]Mirror(nil), m.mirrors...)
	auto := m.auto
	selected := m.selected
	m.mu.Unlock()
	if len(mirrors) == 0 {
		return []source{direct}
	}
	if selected != "" {
		out := make([]source, 0, len(mirrors)+1)
		var picked *Mirror
		for i := range mirrors {
			if mirrors[i].Template == selected {
				picked = &mirrors[i]
				break
			}
		}
		if picked != nil {
			out = append(out, source{label: picked.Name, url: strings.ReplaceAll(picked.Template, "{url}", original)})
		}
		for _, mir := range mirrors {
			if mir.Template == selected {
				continue
			}
			out = append(out, source{label: mir.Name, url: strings.ReplaceAll(mir.Template, "{url}", original)})
		}
		out = append(out, direct)
		return out
	}

	type probe struct {
		source source
		delay  time.Duration
		ok     bool
	}
	candidates := make([]probe, 0, len(mirrors)+1)
	candidates = append(candidates, probe{source: direct})
	for _, mir := range mirrors {
		u := strings.ReplaceAll(mir.Template, "{url}", original)
		candidates = append(candidates, probe{source: source{label: mir.Name, url: u}})
	}

	if !auto {
		// Manual mode keeps the configured order; direct stays first.
		out := []source{direct}
		for _, mir := range mirrors {
			out = append(out, source{label: mir.Name, url: strings.ReplaceAll(mir.Template, "{url}", original)})
		}
		return out
	}

	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			delay, ok := m.probeURL(candidates[i].source.url)
			candidates[i].delay, candidates[i].ok = delay, ok
		}(i)
	}
	wg.Wait()

	if !auto {
		// Manual mode keeps the configured order; direct stays first.
		out := []source{direct}
		for _, mir := range mirrors {
			out = append(out, source{label: mir.Name, url: strings.ReplaceAll(mir.Template, "{url}", original)})
		}
		return out
	}

	// Auto mode: fastest reachable source first, dead sources dropped.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.ok != b.ok {
			return a.ok
		}
		return a.delay < b.delay
	})
	out := make([]source, 0, len(candidates))
	for _, c := range candidates {
		if c.ok {
			out = append(out, c.source)
		}
	}
	if len(out) == 0 {
		// Probes can be fooled by HEAD filtering; keep a usable fallback chain.
		out = append(out, direct)
		for _, mir := range mirrors {
			out = append(out, source{label: mir.Name, url: strings.ReplaceAll(mir.Template, "{url}", original)})
		}
	}
	return out
}

func (m *Manager) probeURL(u string) (time.Duration, bool) {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "https" {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("User-Agent", "komi-go/0.3")
	start := time.Now()
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, false
	}
	resp.Body.Close()
	delay := time.Since(start)
	ok := resp.StatusCode < 400 || resp.StatusCode == http.StatusMethodNotAllowed
	return delay, ok
}

// TestMirrors probes a fixed public asset through every supplied mirror and
// returns latency in milliseconds. Probes run in parallel and never block any
// active download.
func (m *Manager) TestMirrors(ctx context.Context, mirrors []Mirror, target string) []MirrorResult {
	if target == "" {
		target = MirrorProbeURL
	}
	results := make([]MirrorResult, len(mirrors))
	var wg sync.WaitGroup
	for i, mir := range mirrors {
		wg.Add(1)
		go func(i int, mir Mirror) {
			defer wg.Done()
			u := strings.ReplaceAll(mir.Template, "{url}", target)
			r := MirrorResult{Name: mir.Name, Template: mir.Template, Latency: -1}
			parsed, perr := url.Parse(u)
			if perr != nil || parsed.Scheme != "https" {
				r.Error = "仅支持 https"
				results[i] = r
				return
			}
			pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(pctx, http.MethodHead, u, nil)
			if err != nil {
				r.Error = err.Error()
				results[i] = r
				return
			}
			req.Header.Set("User-Agent", "komi-go/0.3")
			start := time.Now()
			resp, err := m.client.Do(req)
			if err != nil {
				r.Error = friendlyProbeError(err)
				results[i] = r
				return
			}
			resp.Body.Close()
			r.Latency = time.Since(start).Milliseconds()
			r.OK = resp.StatusCode < 400 || resp.StatusCode == http.StatusMethodNotAllowed
			if !r.OK {
				r.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			results[i] = r
		}(i, mir)
	}
	wg.Wait()
	return results
}

func friendlyProbeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	msg := err.Error()
	if strings.Contains(msg, "refused") {
		return "连接被拒绝"
	}
	if strings.Contains(msg, "no such host") {
		return "域名解析失败"
	}
	if strings.Contains(msg, "certificate") {
		return "TLS 证书错误"
	}
	return msg
}

func (m *Manager) fetchOne(ctx context.Context, st *jobState, req Request, rawURL string) (string, bool, error) {
	part := st.info.DestPath + ".part"
	metaPath := part + ".meta"

	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
	}
	if offset > 0 {
		var meta partialMeta
		if data, err := os.ReadFile(metaPath); err == nil {
			_ = json.Unmarshal(data, &meta)
		}
		if meta.URL != "" && meta.URL != rawURL {
			_ = os.Remove(part)
			_ = os.Remove(metaPath)
			offset = 0
		}
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", false, err
	}
	httpReq.Header.Set("User-Agent", "komi-go/0.2")
	if isGitHubHost(httpReq.URL) && m.tokenValue() != "" {
		httpReq.Header.Set("Authorization", "Bearer "+m.tokenValue())
	}
	if strings.Contains(httpReq.URL.Host, "api.github.com") {
		httpReq.Header.Set("Accept", "application/octet-stream")
	}
	if offset > 0 {
		httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := m.client.Do(httpReq)
	if err != nil {
		return "", false, netErr(err)
	}
	defer resp.Body.Close()

	appendMode := false
	switch {
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		_ = os.Remove(metaPath)
		offset = 0
		return "", false, errors.New("续传范围失效，已清除临时文件")
	case resp.StatusCode == http.StatusOK:
		offset = 0
	case resp.StatusCode == http.StatusPartialContent:
		appendMode = true
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return "", false, errors.New("资产已不存在，可能被替换或删除")
	default:
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return "", false, fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
		}
	}

	total := req.Asset.Size
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i > 0 {
			if n, err := parseI64(cr[i+1:]); err == nil && n > 0 {
				total = n
			}
		}
	} else if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	if total > 0 {
		st.mu.Lock()
		st.info.Total = total
		st.mu.Unlock()
	}

	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return "", false, fmt.Errorf("无法写入下载目录：%w", err)
	}
	defer out.Close()

	done := offset
	if !appendMode {
		done = 0
	}
	buf := make([]byte, 64*1024)
	lastEmit := time.Now()

	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	watchdog := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchdog:
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastRead.Load())) > 60*time.Second {
					cancel()
					return
				}
			}
		}
	}()
	defer close(watchdog)

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			lastRead.Store(time.Now().UnixNano())
			if _, werr := out.Write(buf[:n]); werr != nil {
				m.saveMeta(metaPath, rawURL, resp.Header.Get("ETag"), total)
				return "", false, fmt.Errorf("写入失败：%w", werr)
			}
			done += int64(n)
			if time.Since(lastEmit) > 150*time.Millisecond {
				lastEmit = time.Now()
				m.progress(st, done, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			m.saveMeta(metaPath, rawURL, resp.Header.Get("ETag"), total)
			if reqCtx.Err() != nil {
				return "", false, context.Canceled
			}
			return "", false, netErr(readErr)
		}
	}
	if err := out.Close(); err != nil {
		return "", false, err
	}
	m.saveMeta(metaPath, rawURL, resp.Header.Get("ETag"), total)
	m.progress(st, done, total)

	m.updateStatus(st, ledger.StatusVerifying, "")
	digest := req.Asset.Digest
	verified := false
	if strings.TrimSpace(digest) == "" {
		if side, err := m.sidecarDigest(req.Asset.DownloadURL); err == nil && side != "" {
			digest = side
			req.Asset.Digest = side
			st.mu.Lock()
			st.info.Digest = side
			st.mu.Unlock()
		}
	}
	if digest != "" {
		if err := verifyDigest(part, digest); err != nil {
			m.saveMeta(metaPath, rawURL, resp.Header.Get("ETag"), total)
			return "", false, err
		}
		verified = true
	}
	final := st.info.DestPath
	if fileExists(final) {
		final = platform.UniquePath(filepath.Dir(final), filepath.Base(final))
	}
	if err := os.Rename(part, final); err != nil {
		return "", false, fmt.Errorf("重命名失败：%w", err)
	}
	_ = os.Remove(metaPath)
	return final, verified, nil
}

// sidecarDigest tries <asset>.sha256 and parses the first 64 hex characters.
func (m *Manager) sidecarDigest(rawURL string) (string, error) {
	u, err := url.Parse(rawURL + ".sha256")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "komi-go/0.2")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("没有附属校验文件")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	match := regexp.MustCompile(`[a-fA-F0-9]{64}`).FindString(string(data))
	if match == "" {
		return "", errors.New("附属文件里没有 SHA-256")
	}
	return "sha256:" + strings.ToLower(match), nil
}

// ---------------------------------------------------------------------------
// state transitions
// ---------------------------------------------------------------------------

func (m *Manager) progress(st *jobState, done, total int64) {
	st.mu.Lock()
	st.info.Done = done
	if total > 0 {
		st.info.Total = total
		p := int(done * 100 / total)
		if p > 100 {
			p = 100
		}
		st.info.Percent = p
	}
	snapshot := st.info
	st.mu.Unlock()
	m.emit("download:progress", snapshot)
	if m.ledger != nil && st.reasonOf() == "" {
		_ = m.ledger.UpdateTaskProgress(context.Background(), snapshot.ID, done, total, ledger.StatusDownloading, "")
	}
}

func (m *Manager) updateStatus(st *jobState, status, msg string) {
	st.mu.Lock()
	st.info.Status = status
	st.info.Error = msg
	st.info.CanResume = status == ledger.StatusPaused || status == ledger.StatusFailed
	snapshot := st.info
	st.mu.Unlock()
	m.emit("download:changed", snapshot)
	if m.ledger != nil && st.reasonOf() != "cancel" {
		_ = m.ledger.UpdateTaskProgress(context.Background(), snapshot.ID, snapshot.Done, snapshot.Total, status, msg)
	}
}

// Pause stops a running download, keeping the .part for resume.
func (m *Manager) Pause(id string) error {
	st := m.find(id)
	if st == nil {
		return errors.New("任务不存在")
	}
	if st.reasonOf() == "" {
		st.setReason("pause")
		if cancel := st.cancelFunc(); cancel != nil {
			cancel()
		}
	}
	m.updateStatus(st, ledger.StatusPaused, "已暂停，可继续下载")
	return nil
}

// Resume restarts a paused or failed job with its original request.
func (m *Manager) Resume(id string) (Job, error) {
	st := m.find(id)
	if st == nil {
		return Job{}, errors.New("任务不存在")
	}
	if st.reasonOf() == "cancel" {
		return Job{}, errors.New("任务已取消")
	}
	st.setReason("")
	st.mu.Lock()
	oldDone := st.done
	oldCancel := st.cancel
	ctx, cancel := context.WithCancel(context.Background())
	st.cancel = cancel
	st.done = make(chan struct{})
	st.info.Status = ledger.StatusQueued
	st.info.Error = ""
	st.mu.Unlock()

	// Wait for the previous worker before touching the same .part file again.
	if oldCancel != nil {
		oldCancel()
	}
	if oldDone != nil {
		select {
		case <-oldDone:
		case <-time.After(8 * time.Second):
			return Job{}, errors.New("上一段下载仍在收尾，请稍后再试")
		}
	}
	st.setReason("")
	m.launch(ctx, st)
	return snapshot(st), nil
}

// Cancel aborts the job, deletes the partial file and forgets the queue row.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	st, ok := m.jobs[id]
	if ok {
		delete(m.jobs, id)
	}
	m.mu.Unlock()
	if !ok {
		return errors.New("任务不存在")
	}
	st.setReason("cancel")
	st.mu.Lock()
	cancel := st.cancel
	done := st.done
	st.mu.Unlock()
	if cancel != nil {
		cancel()
		if done != nil {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}
	}
	part := st.info.DestPath + ".part"
	_ = os.Remove(part)
	_ = os.Remove(part + ".meta")
	_ = os.Remove(st.info.DestPath)
	if m.ledger != nil {
		_ = m.ledger.DeleteTask(context.Background(), id)
	}
	m.emit("download:removed", id)
	return nil
}

// Forget drops a finished/failed job from the queue without touching the
// downloaded file. Used by "清理已完成".
func (m *Manager) Forget(id string) error {
	m.mu.Lock()
	st, ok := m.jobs[id]
	if ok {
		delete(m.jobs, id)
	}
	m.mu.Unlock()
	if !ok {
		return errors.New("任务不存在")
	}
	st.mu.Lock()
	status := st.info.Status
	st.mu.Unlock()
	switch status {
	case ledger.StatusDone, ledger.StatusFailed, ledger.StatusCanceled, ledger.StatusUnverified:
	default:
		return errors.New("任务仍在进行，不能清理")
	}
	if m.ledger != nil {
		_ = m.ledger.DeleteTask(context.Background(), id)
	}
	m.emit("download:removed", id)
	return nil
}

// DiskFree reports the available bytes on the volume that holds dir.
func DiskFree(dir string) (uint64, error) { return diskFree(dir) }

// List returns a snapshot of all known jobs.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, st := range m.jobs {
		out = append(out, snapshot(st))
	}
	return out
}

// Shutdown cancels everything and waits for goroutines to exit.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, st := range m.jobs {
		if st.reasonOf() == "" {
			st.setReason("shutdown")
			if cancel := st.cancelFunc(); cancel != nil {
				cancel()
			}
		}
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

// Restore rebuilds the queue from SQLite after a restart. Active rows resume
// automatically; paused/failed rows wait for the user.
func (m *Manager) Restore(tasks []ledger.DownloadTask) {
	for _, task := range tasks {
		switch task.Status {
		case ledger.StatusDone:
			if !fileExists(task.DestPath) {
				continue
			}
			m.mu.Lock()
			m.jobs[task.ID] = &jobState{info: Job{
				ID: task.ID, RepoKey: task.RepoKey, DisplayName: task.DisplayName,
				AssetName: task.AssetName, AssetID: task.AssetID, AssetURL: task.AssetURL, Digest: task.Digest,
				Tag: task.Tag, PublishedAt: task.PublishedAt, Status: ledger.StatusDone,
				DestPath: task.DestPath, Total: task.Total, Done: task.Total, Percent: 100,
				Version: task.Version, Verified: task.Verified,
			}}
			m.mu.Unlock()
		case ledger.StatusQueued, ledger.StatusDownloading, ledger.StatusPaused,
			ledger.StatusFailed, ledger.StatusVerifying:
			req := Request{
				RepoKey: task.RepoKey,
				Asset: catalog.Asset{
					ID: task.AssetID, Name: task.AssetName, Size: task.Total,
					DownloadURL: task.AssetURL, Digest: task.Digest,
				},
				DisplayName: task.DisplayName, Tag: task.Tag, PublishedAt: task.PublishedAt,
			}
			st := &jobState{
				info: Job{
					ID: task.ID, RepoKey: task.RepoKey, DisplayName: task.DisplayName,
					AssetName: task.AssetName, AssetID: task.AssetID, AssetURL: task.AssetURL, Digest: task.Digest,
					Tag: task.Tag, PublishedAt: task.PublishedAt, Status: task.Status,
					DestPath: task.DestPath, Total: task.Total, Done: task.Done,
					Version: task.Version, Verified: task.Verified,
				},
				req: req, done: make(chan struct{}),
			}
			if task.Status == ledger.StatusPaused || task.Status == ledger.StatusFailed {
				st.info.CanResume = true
				m.mu.Lock()
				m.jobs[task.ID] = st
				m.mu.Unlock()
				m.emit("download:added", st.info)
				continue
			}
			m.mu.Lock()
			m.jobs[task.ID] = st
			m.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			st.cancel = cancel
			m.emit("download:added", st.info)
			m.launch(ctx, st)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func snapshot(st *jobState) Job {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.info
}

func (st *jobState) reasonOf() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.reason
}

func (st *jobState) setReason(reason string) {
	st.mu.Lock()
	st.reason = reason
	st.mu.Unlock()
}

func (st *jobState) cancelFunc() context.CancelFunc {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cancel
}

func (m *Manager) find(id string) *jobState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (m *Manager) persist(st *jobState, status string) {
	if m.ledger == nil {
		return
	}
	info := snapshot(st)
	_ = m.ledger.UpsertTask(context.Background(), ledger.DownloadTask{
		ID: info.ID, RepoKey: info.RepoKey, DisplayName: info.DisplayName,
		AssetName: info.AssetName, AssetID: info.AssetID, AssetURL: st.req.Asset.DownloadURL,
		FileName: filepath.Base(info.DestPath), DestPath: info.DestPath,
		Total: info.Total, Done: info.Done, Status: status,
		Digest: info.Digest, Tag: info.Tag, Version: info.Version,
		PublishedAt: info.PublishedAt, Verified: info.Verified,
		CreatedAt: time.Now().Unix(),
	})
}

func (m *Manager) saveMeta(path, rawURL, etag string, total int64) {
	data, _ := json.Marshal(partialMeta{URL: rawURL, ETag: etag, Total: total})
	_ = os.WriteFile(path, data, 0o644)
}

type partialMeta struct {
	URL   string `json:"url"`
	ETag  string `json:"etag"`
	Total int64  `json:"total"`
}

func (m *Manager) tokenValue() string {
	if m.token == nil {
		return ""
	}
	return strings.TrimSpace(m.token())
}

func jobID(repoKey string, assetID int64, rawURL string) string {
	return fmt.Sprintf("%s-%d-%s", sanitizeID(repoKey), assetID, shortHash(rawURL))
}

func verifyDigest(path, digest string) error {
	algo, want, ok := strings.Cut(strings.ToLower(strings.TrimSpace(digest)), ":")
	if !ok || algo != "sha256" || want == "" {
		return errors.New("不支持的校验算法")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return errors.New("SHA-256 校验失败，文件可能被篡改或下载不完整")
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sanitizeID(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-", "#", "-").Replace(s)
	if s == "" {
		return "asset"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func parseI64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(strings.TrimSpace(s), &n)
	return n, err
}

func isGitHubHost(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	return h == "github.com" || h == "api.github.com" ||
		h == "objects.githubusercontent.com" || strings.HasSuffix(h, ".githubusercontent.com")
}

func netErr(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return errors.New("网络超时，可稍后继续下载")
	}
	return fmt.Errorf("网络错误：%w", err)
}

func friendly(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// diskFree uses GetDiskFreeSpaceExW on the download volume.
func diskFree(dir string) (uint64, error) {
	ptr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	var freeAvail, total, freeTotal uint64
	r, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&freeTotal)),
	)
	if r == 0 {
		return 0, callErr
	}
	return freeAvail, nil
}
