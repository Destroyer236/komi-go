package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"komi-go/internal/catalog"
)

// --- helpers ---

type rangeRecorder struct {
	mu            sync.Mutex
	ranges        []string
	inFlight      int
	maxConcurrent int
}

func (r *rangeRecorder) enter() {
	r.mu.Lock()
	r.inFlight++
	if r.inFlight > r.maxConcurrent {
		r.maxConcurrent = r.inFlight
	}
	r.mu.Unlock()
}

func (r *rangeRecorder) leave() {
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
}

func (r *rangeRecorder) add(spec string) {
	r.mu.Lock()
	r.ranges = append(r.ranges, spec)
	r.mu.Unlock()
}

func (r *rangeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ranges)
}

func (r *rangeRecorder) peak() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxConcurrent
}

func (r *rangeRecorder) specs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ranges...)
}

// startSegmentServer serves real 206 responses for "bytes=start-end" and
// reports how many requests were in flight at once, so a sequential
// implementation cannot pass as a parallel one.
func startSegmentServer(t *testing.T, body []byte, ignoreRange bool, chunkDelay time.Duration) (*httptest.Server, *rangeRecorder) {
	t.Helper()
	rec := &rangeRecorder{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != assetPath {
			http.Error(w, "not the asset", http.StatusInternalServerError)
			return
		}
		rec.enter()
		defer rec.leave()
		start, end, ok := parseContentRange("bytes " + strings.TrimPrefix(r.Header.Get("Range"), "bytes=") + "/")
		if ignoreRange || !ok {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		rec.add(r.Header.Get("Range"))
		if start < 0 || start >= int64(len(body)) || end < start {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end > int64(len(body))-1 {
			// Clamp an over-long request: mirrors sometimes do this.
			end = int64(len(body)) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", strconv.Itoa(int(end-start+1)))
		w.WriteHeader(http.StatusPartialContent)
		flusher, _ := w.(http.Flusher)
		const chunk = 16 << 10
		for off := start; off <= end; {
			stop := off + chunk
			if stop > end+1 {
				stop = end + 1
			}
			if _, err := w.Write(body[off:stop]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			off = stop
			// Slow enough that progress is observable and pausing is possible.
			if chunkDelay > 0 {
				time.Sleep(chunkDelay)
			}
		}
	}))
	return srv, rec
}

func segmentBody(size int) []byte {
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 251)
	}
	return body
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newSegmentManager(t *testing.T, srv *httptest.Server, connections int) *Manager {
	t.Helper()
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	m.SetAuto(false) // direct only: mirrors would rewrite the path
	m.SetConnections(connections)
	m.SetHTTPClient(testClient(srv))
	return m
}

// --- plan math ---

func TestSegmentCountHonoursMinimumAndCap(t *testing.T) {
	cases := []struct {
		total int64
		want  int
		exp   int
	}{
		{0, 8, 1},
		{100 << 10, 512, 1},    // below one minimum slice
		{2 << 20, 8, 8},        // 8 slices of 256 KiB
		{2 << 20, 512, 8},      // capped by the minimum slice
		{300 << 20, 512, 512},  // the product ceiling
		{300 << 20, 1000, 512}, // request clamped to MaxConnections
		{5 << 20, 0, 1},        // a zero request still downloads
	}
	for _, c := range cases {
		if got := segmentCount(c.total, c.want); got != c.exp {
			t.Errorf("segmentCount(%d, %d) = %d, want %d", c.total, c.want, got, c.exp)
		}
	}
}

func TestPlanSegmentsCoversWholeFile(t *testing.T) {
	segs := planSegments(10, 3)
	if len(segs) != 3 {
		t.Fatalf("got %d segments", len(segs))
	}
	want := []segment{{Start: 0, End: 2}, {Start: 3, End: 5}, {Start: 6, End: 9}}
	for i := range want {
		if segs[i] != want[i] {
			t.Fatalf("segment %d = %+v, want %+v", i, segs[i], want[i])
		}
	}
	if got := usableSegments(segs, 10); len(got) != 3 {
		t.Fatalf("a complete map was rejected: %+v", got)
	}
}

func TestUsableSegmentsRejectsBadMaps(t *testing.T) {
	good := planSegments(8, 4)
	if got := usableSegments(good, 8); len(got) != 4 {
		t.Fatalf("complete map rejected: %+v", got)
	}
	gap := []segment{{Start: 0, End: 1}, {Start: 3, End: 7}}
	if got := usableSegments(gap, 8); got != nil {
		t.Fatal("a map with a gap was accepted")
	}
	overflow := append([]segment(nil), good...)
	if got := usableSegments(overflow, 6); got != nil {
		t.Fatal("a map past the asset size was accepted")
	}
	badDone := append([]segment(nil), good...)
	badDone[1].Done = badDone[1].size() + 1
	if got := usableSegments(badDone, 8); got != nil {
		t.Fatal("an impossible Done was accepted")
	}
	if got := usableSegments(good, 0); got != nil {
		t.Fatal("unknown total accepted a map")
	}
}

func TestSetConnectionsClamps(t *testing.T) {
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	if m.Connections() != DefaultConnections {
		t.Fatalf("default = %d", m.Connections())
	}
	if got := m.SetConnections(0); got != 1 {
		t.Fatalf("SetConnections(0) = %d, want 1", got)
	}
	if got := m.SetConnections(9999); got != MaxConnections {
		t.Fatalf("SetConnections(9999) = %d, want %d", got, MaxConnections)
	}
	if got := m.SetConnections(300); got != 300 {
		t.Fatalf("SetConnections(300) = %d", got)
	}
}

// --- transfers ---

func TestSegmentedDownloadRunsInParallel(t *testing.T) {
	body := segmentBody(2 << 20)
	srv, rec := startSegmentServer(t, body, false, 15*time.Millisecond)
	defer srv.Close()

	m := newSegmentManager(t, srv, 8)
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset: catalog.Asset{
			Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL,
			Digest: digestOf(body),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForStatus(t, m, job.ID, "done")
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, body) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(data), len(body))
	}
	snap := findJob(t, m, job.ID)
	if !snap.Verified {
		t.Fatal("digest was not verified for the segmented transfer")
	}
	if snap.Source != DirectSourceLabel {
		t.Fatalf("source = %q", snap.Source)
	}
	if got := rec.count(); got != 8 {
		t.Fatalf("issued %d range requests, want 8: %v", got, rec.specs())
	}
	if peak := rec.peak(); peak < 2 {
		t.Fatalf("peak concurrency was %d; the transfer was serial", peak)
	}
	// Each request must start where its slice starts, otherwise the file would
	// be assembled from overlapping writes.
	starts := map[string]bool{}
	for _, spec := range rec.specs() {
		starts[strings.Split(strings.TrimPrefix(spec, "bytes="), "-")[0]] = true
	}
	if len(starts) != 8 {
		t.Fatalf("range starts overlap: %v", rec.specs())
	}
}

// A mirror that strips Range answers 200 with the whole file; the job must
// still finish in one stream instead of failing.
func TestSegmentedFallsBackWhenServerIgnoresRange(t *testing.T) {
	body := segmentBody(2 << 20)
	srv, _ := startSegmentServer(t, body, true, 0)
	defer srv.Close()

	m := newSegmentManager(t, srv, 8)
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset: catalog.Asset{
			Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL,
			Digest: digestOf(body),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForStatus(t, m, job.ID, "done")
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, body) {
		t.Fatalf("fallback produced %d bytes, want %d", len(data), len(body))
	}
}

// Pause mid-flight, then resume: the per-segment map has to survive the restart
// so only the missing tails are re-fetched.
func TestSegmentedPauseResumeKeepsSegmentMap(t *testing.T) {
	body := segmentBody(8 << 20)
	srv, _ := startSegmentServer(t, body, false, 12*time.Millisecond)
	defer srv.Close()

	m := newSegmentManager(t, srv, 8)
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset: catalog.Asset{
			Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL,
			Digest: digestOf(body),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitUntilProgress(t, m, job.ID)
	if err := m.Pause(job.ID); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, m, job.ID, "paused")

	part := findJob(t, m, job.ID).DestPath + ".part"
	raw, err := os.ReadFile(part + ".meta")
	if err != nil {
		t.Fatalf("segment map was not persisted: %v", err)
	}
	var meta partialMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Segments) != 8 {
		t.Fatalf("persisted %d segments, want 8", len(meta.Segments))
	}
	done := int64(0)
	for _, s := range meta.Segments {
		done += s.Done
	}
	if done == 0 {
		t.Fatal("no progress was recorded in the map")
	}
	if _, err := m.Resume(job.ID); err != nil {
		t.Fatal(err)
	}
	final := waitForStatus(t, m, job.ID, "done")
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, body) {
		t.Fatalf("resumed file has %d bytes, want %d", len(data), len(body))
	}
}
