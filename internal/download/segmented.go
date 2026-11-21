package download

// Segmented (multi-connection) transfers.
//
// A single TCP stream rarely saturates a fast line: GitHub release assets and
// most mirrors throttle per connection, so asking for N byte ranges in
// parallel is the standard way to get four-digit MB/s. The plan is split from
// the asset size the release API already gave us, every worker writes into its
// own offset of one .part file, and the map is persisted so a pause or a crash
// resumes only the missing tails.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"komi-go/internal/ledger"
	"komi-go/internal/platform"
)

const (
	// MaxConnections caps parallel range requests per job. 512 is the ceiling
	// from the product spec; above that the handshake cost dominates.
	MaxConnections = 512
	// DefaultConnections keeps a fresh install fast without being rude: eight
	// streams already saturate a typical home line.
	DefaultConnections = 8
	// minSegmentSize is the smallest slice worth its own connection. Without
	// this floor every small asset would open hundreds of sockets.
	minSegmentSize = 256 << 10
	// segmentAttempts is per-segment, so one flaky mirror hop does not fail a
	// whole multi-gigabyte transfer.
	segmentAttempts = 3
)

// errNoRangeSupport tells the caller the server ignored Range and answered
// 200, so the only sane move is one plain stream.
var errNoRangeSupport = errors.New("服务器不支持分段下载")

// segment is one byte range of the asset plus how much of it is on disk.
type segment struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Done  int64 `json:"done"`
}

func (s segment) size() int64 { return s.End - s.Start + 1 }

// segRun is the live view of a segment: bytes already on disk before this
// attempt plus the bytes written by it.
type segRun struct {
	seg  segment
	base int64
	done atomic.Int64
}

func (r *segRun) progress() int64 { return r.base + r.done.Load() }

// segmentCount decides how many connections an asset deserves.
func segmentCount(total int64, want int) int {
	if want < 1 {
		want = 1
	}
	if want > MaxConnections {
		want = MaxConnections
	}
	if total <= 0 {
		return 1
	}
	bySize := int((total + minSegmentSize - 1) / minSegmentSize)
	if bySize < 1 {
		bySize = 1
	}
	if want > bySize {
		want = bySize
	}
	return want
}

// planSegments splits [0,total) into n contiguous ranges.
func planSegments(total int64, n int) []segment {
	if n < 1 {
		n = 1
	}
	size := total / int64(n)
	segs := make([]segment, 0, n)
	for i := 0; i < n; i++ {
		start := int64(i) * size
		end := start + size - 1
		if i == n-1 {
			end = total - 1
		}
		if start > end {
			continue
		}
		segs = append(segs, segment{Start: start, End: end})
	}
	return segs
}

// usableSegments returns a persisted map only when it covers the whole asset
// exactly once; anything else (older single-stream meta, a different size,
// tampering) is thrown away and the download restarts.
func usableSegments(segs []segment, total int64) []segment {
	if len(segs) == 0 || total <= 0 {
		return nil
	}
	ordered := append([]segment(nil), segs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	var next int64
	for _, s := range ordered {
		if s.Start != next || s.End < s.Start || s.End > total-1 {
			return nil
		}
		if s.Done < 0 || s.Done > s.size() {
			return nil
		}
		next = s.End + 1
	}
	if next != total {
		return nil
	}
	return ordered
}

func readMeta(path string) partialMeta {
	var meta partialMeta
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	return meta
}

func (m *Manager) saveMetaSegments(path, rawURL, etag string, total int64, segs []segment) {
	data, _ := json.Marshal(partialMeta{URL: rawURL, ETag: etag, Total: total, Segments: segs})
	_ = os.WriteFile(path, data, 0o644)
}

// fetchSegmented downloads every range in parallel and finishes the file the
// same way the single-stream path does (digest, then rename).
func (m *Manager) fetchSegmented(ctx context.Context, st *jobState, req Request, rawURL string, total int64, segs []segment, part, metaPath, etag string) (string, bool, error) {
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", false, fmt.Errorf("无法写入下载目录：%w", err)
	}

	runs := make([]*segRun, 0, len(segs))
	var progress atomic.Int64
	for _, s := range segs {
		runs = append(runs, &segRun{seg: s, base: s.Done})
		progress.Add(s.Done)
	}
	st.mu.Lock()
	st.info.Total = total
	st.mu.Unlock()

	gctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		firstErr error
		errOnce  sync.Once
		wg       sync.WaitGroup
	)
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())

	watchdog := make(chan struct{})
	go func() {
		ticker := time.NewTicker(watchInterval())
		defer ticker.Stop()
		for {
			select {
			case <-watchdog:
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastRead.Load())) > stallAfter {
					st.setStalled(true)
					cancel()
					return
				}
			}
		}
	}()
	defer close(watchdog)

	reporter := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-reporter:
				m.progress(st, progress.Load(), total)
				return
			case <-ticker.C:
				m.progress(st, progress.Load(), total)
				m.saveMetaSegments(metaPath, rawURL, etag, total, snapshotSegments(runs))
			}
		}
	}()
	defer close(reporter)

	for _, r := range runs {
		if r.seg.size() <= r.base {
			continue
		}
		wg.Add(1)
		go func(r *segRun) {
			defer wg.Done()
			if err := m.runSegment(gctx, rawURL, out, r, &progress, &lastRead); err != nil && !errors.Is(err, context.Canceled) {
				errOnce.Do(func() { firstErr = err })
				cancel()
			}
		}(r)
	}
	wg.Wait()

	if firstErr != nil {
		_ = out.Close()
		return "", false, firstErr
	}
	if ctx.Err() != nil {
		// pause / cancel / stall: keep the .part and its map for resume
		m.saveMetaSegments(metaPath, rawURL, etag, total, snapshotSegments(runs))
		_ = out.Close()
		return "", false, context.Canceled
	}
	if got := progress.Load(); got < total {
		_ = out.Close()
		return "", false, fmt.Errorf("分段下载不完整：%d/%d 字节", got, total)
	}
	if err := out.Close(); err != nil {
		return "", false, err
	}
	m.saveMetaSegments(metaPath, rawURL, etag, total, snapshotSegments(runs))
	return m.finalize(st, req, part, metaPath)
}

func snapshotSegments(runs []*segRun) []segment {
	out := make([]segment, 0, len(runs))
	for _, r := range runs {
		s := r.seg
		s.Done = r.progress()
		out = append(out, s)
	}
	return out
}

func watchInterval() time.Duration {
	interval := 2 * time.Second
	if stallAfter/2 < interval {
		interval = stallAfter / 2
	}
	if interval < 50*time.Millisecond {
		interval = 50 * time.Millisecond
	}
	return interval
}

// runSegment fetches the missing tail of one range, retrying transient errors.
func (m *Manager) runSegment(ctx context.Context, rawURL string, out *os.File, r *segRun, progress, lastRead *atomic.Int64) error {
	var lastErr error
	for attempt := 0; attempt < segmentAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return context.Canceled
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		from := r.seg.Start + r.progress()
		if from > r.seg.End {
			return nil
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		httpReq.Header.Set("User-Agent", "komi-go/0.2")
		httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, r.seg.End))
		if isGitHubHost(httpReq.URL) && m.tokenValue() != "" {
			httpReq.Header.Set("Authorization", "Bearer "+m.tokenValue())
		}
		resp, err := m.client.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return context.Canceled
			}
			lastErr = netErr(err)
			continue
		}
		switch resp.StatusCode {
		case http.StatusOK:
			// The server ignored Range. Only a single stream can work.
			resp.Body.Close()
			return errNoRangeSupport
		case http.StatusRequestedRangeNotSatisfiable:
			resp.Body.Close()
			return errors.New("分段范围失效，需要重新下载")
		case http.StatusNotFound, http.StatusGone:
			resp.Body.Close()
			return errors.New("资产已不存在，可能被替换或删除")
		case http.StatusPartialContent:
		default:
			resp.Body.Close()
			lastErr = fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
			continue
		}
		if start, _, ok := parseContentRange(resp.Header.Get("Content-Range")); ok && start != from {
			resp.Body.Close()
			lastErr = errors.New("服务器返回的分段范围不匹配")
			continue
		}
		err = copyRange(ctx, resp.Body, out, r, progress, lastRead)
		resp.Body.Close()
		if err == nil {
			if r.progress() >= r.seg.size() {
				return nil
			}
			// Some CDNs clamp Content-Range shorter than asked; keep going for
			// the tail instead of declaring the segment finished.
			lastErr = errors.New("分段提前结束")
			continue
		}
		if errors.Is(err, context.Canceled) {
			return err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("分段下载失败")
	}
	return lastErr
}

// copyRange writes the body at the segment offset; WriteAt means connections
// never contend for a file position.
func copyRange(ctx context.Context, body io.Reader, out *os.File, r *segRun, progress, lastRead *atomic.Int64) error {
	buf := make([]byte, 64<<10)
	offset := r.seg.Start + r.progress()
	for {
		if ctx.Err() != nil {
			return context.Canceled
		}
		n, readErr := body.Read(buf)
		if n > 0 {
			lastRead.Store(time.Now().UnixNano())
			if remain := r.seg.End + 1 - offset; int64(n) > remain {
				n = int(remain)
			}
			if n > 0 {
				if _, werr := out.WriteAt(buf[:n], offset); werr != nil {
					return fmt.Errorf("写入失败：%w", werr)
				}
				offset += int64(n)
				r.done.Add(int64(n))
				progress.Add(int64(n))
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return context.Canceled
			}
			return netErr(readErr)
		}
	}
}

// parseContentRange reads "bytes <start>-<end>/<total>".
func parseContentRange(header string) (int64, int64, bool) {
	h := strings.TrimSpace(header)
	if !strings.HasPrefix(h, "bytes ") {
		return 0, 0, false
	}
	h = strings.TrimPrefix(h, "bytes ")
	dash := strings.Index(h, "-")
	slash := strings.Index(h, "/")
	if dash <= 0 || slash <= dash {
		return 0, 0, false
	}
	start, err := parseI64(h[:dash])
	if err != nil {
		return 0, 0, false
	}
	end, err := parseI64(h[dash+1 : slash])
	if err != nil {
		return 0, 0, false
	}
	return start, end, true
}

// finalize verifies the digest (falling back to a .sha256 sidecar) and moves
// the part file into place. Shared by both transfer modes.
func (m *Manager) finalize(st *jobState, req Request, part, metaPath string) (string, bool, error) {
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
			return "", false, err
		}
		verified = true
	}
	st.mu.Lock()
	final := st.info.DestPath
	st.mu.Unlock()
	if fileExists(final) {
		final = platform.UniquePath(filepath.Dir(final), filepath.Base(final))
	}
	if err := os.Rename(part, final); err != nil {
		return "", false, fmt.Errorf("重命名失败：%w", err)
	}
	_ = os.Remove(metaPath)
	return final, verified, nil
}
