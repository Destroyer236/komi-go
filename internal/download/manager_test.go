package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"komi-go/internal/catalog"
)

const testAssetURL = "https://github.com/o/r/releases/download/v1/app.exe"

func TestSetMirrorsFiltersInvalid(t *testing.T) {
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	m.SetMirrors([]Mirror{
		{Name: "ok", Template: " https://ghproxy.example/https://github.com/{url} "},
		{Name: "empty", Template: "  "},
		{Name: "http", Template: "http://mirror.example/{url}"},
		{Name: "no-placeholder", Template: "https://mirror.example/fetch"},
		{Name: "", Template: "https://second.example/{url}"},
	})
	if len(m.mirrors) != 2 {
		t.Fatalf("mirrors = %#v", m.mirrors)
	}
	if !strings.Contains(m.mirrors[0].Template, "{url}") {
		t.Fatalf("first mirror = %#v", m.mirrors[0])
	}
}

func TestStartRejectsBadURL(t *testing.T) {
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	_, err := m.Start(Request{
		RepoKey: "o/r",
		Asset:   catalog.Asset{Name: "app.exe", DownloadURL: "http://example.com/app.exe"},
	})
	if err == nil {
		t.Fatal("expected http URL to be rejected")
	}
}

func TestPresetMirrorsAreValid(t *testing.T) {
	list := PresetMirrors()
	if len(list) < 4 {
		t.Fatalf("presets = %d, want >= 4", len(list))
	}
	for _, m := range list {
		if !strings.HasPrefix(m.Template, "https://") || !strings.Contains(m.Template, "{url}") {
			t.Fatalf("bad preset %#v", m)
		}
		if strings.TrimSpace(m.Name) == "" {
			t.Fatalf("unnamed preset %#v", m)
		}
	}
}

func TestTestMirrorsRejectsInvalidURL(t *testing.T) {
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	r := m.TestMirrors(t.Context(), []Mirror{{Name: "bad", Template: "http://x/{url}"}}, "https://example.com/a.zip")
	if len(r) != 1 || r[0].OK || r[0].Latency != -1 {
		t.Fatalf("result = %#v", r)
	}
}

func TestSourcesManualAndSelected(t *testing.T) {
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	m.SetAuto(false)
	m.SetMirrors([]Mirror{
		{Name: "alpha", Template: "https://alpha.example/{url}"},
		{Name: "beta", Template: "https://beta.example/{url}"},
	})
	srcs := m.sources(testAssetURL)
	if len(srcs) != 3 || srcs[0].label != DirectSourceLabel {
		t.Fatalf("manual order = %#v", srcs)
	}
	if srcs[1].label != "alpha" || srcs[1].url != "https://alpha.example/"+testAssetURL {
		t.Fatalf("mirror url = %#v", srcs[1])
	}

	m.SetSelected("https://beta.example/{url}")
	srcs = m.sources(testAssetURL)
	if len(srcs) != 3 || srcs[0].label != "beta" {
		t.Fatalf("selected order = %#v", srcs)
	}
	if srcs[len(srcs)-1].label != DirectSourceLabel {
		t.Fatalf("direct should be the fallback, got %#v", srcs)
	}
}

func TestVerifyDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.bin")
	body := []byte("komi-go payload")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if err := verifyDigest(path, "sha256:"+hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	if err := verifyDigest(path, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("mismatched digest accepted")
	}
	if err := verifyDigest(path, "md5:deadbeef"); err == nil {
		t.Fatal("unsupported algorithm accepted")
	}
}

func TestDownloadFullFlow(t *testing.T) {
	body := bytes.Repeat([]byte("komi-go-"), 512)
	srv, rec := startAssetServer(t, body)
	defer srv.Close()
	dir := t.TempDir()
	m := New(dir, nil, func() string { return "" }, nil)
	m.SetHTTPClient(testClient(srv))

	sum := sha256.Sum256(body)
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset: catalog.Asset{
			Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL,
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
		},
		DisplayName: "demo",
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
		t.Fatalf("file content mismatch: %d vs %d bytes", len(data), len(body))
	}
	snap := findJob(t, m, job.ID)
	if !snap.Verified {
		t.Fatal("digest verification not reported")
	}
	if snap.Source != DirectSourceLabel {
		t.Fatalf("source = %q, want %q", snap.Source, DirectSourceLabel)
	}
	if len(rec.ranges) != 0 {
		t.Fatalf("fresh download should not send Range, got %v", rec.ranges)
	}
}

func TestDownloadResumesFromPart(t *testing.T) {
	body := bytes.Repeat([]byte("resume-"), 500)
	srv, rec := startAssetServer(t, body)
	defer srv.Close()
	dir := t.TempDir()
	half := len(body) / 2
	part := filepath.Join(dir, "app.exe.part")
	if err := os.WriteFile(part, body[:half], 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(partialMeta{URL: testAssetURL, Total: int64(len(body))})
	if err := os.WriteFile(part+".meta", meta, 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(dir, nil, func() string { return "" }, nil)
	m.SetHTTPClient(testClient(srv))
	sum := sha256.Sum256(body)
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset: catalog.Asset{
			Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL,
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
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
		t.Fatalf("resumed file mismatch: %d vs %d bytes", len(data), len(body))
	}
	rec.mu.Lock()
	got := append([]string(nil), rec.ranges...)
	rec.mu.Unlock()
	if len(got) != 1 || got[0] != fmt.Sprintf("bytes=%d-", half) {
		t.Fatalf("range requests = %v, want one bytes=%d-", got, half)
	}
}

func TestDownloadFallsBackWhenMirrorFails(t *testing.T) {
	body := bytes.Repeat([]byte("mirror-"), 400)
	srv, rec := startAssetServer(t, body)
	defer srv.Close()
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	m.SetHTTPClient(testClient(srv))
	m.SetAuto(false)
	const tpl = "https://dead.example/{url}"
	m.SetMirrors([]Mirror{{Name: "selected-mirror", Template: tpl}})
	m.SetSelected(tpl)

	sum := sha256.Sum256(body)
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset: catalog.Asset{
			Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL,
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
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
		t.Fatalf("mirror download mismatch: %d vs %d", len(data), len(body))
	}
	snap := findJob(t, m, job.ID)
	if snap.Source != DirectSourceLabel {
		t.Fatalf("source = %q, want direct fallback", snap.Source)
	}
	if !snap.Verified {
		t.Fatal("digest must still be verified after fallback")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.paths) < 2 {
		t.Fatalf("want a mirror attempt then a direct one, got %v", rec.paths)
	}
	if rec.paths[len(rec.paths)-1] != assetPath {
		t.Fatalf("direct request missing, got %v", rec.paths)
	}
}

func TestCancelRemovesPartialFile(t *testing.T) {
	body := bytes.Repeat([]byte("cancel-"), 8192)
	srv := slowAssetServer(t, body)
	defer srv.Close()
	dir := t.TempDir()
	m := New(dir, nil, func() string { return "" }, nil)
	m.SetHTTPClient(testClient(srv))
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset:   catalog.Asset{Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitUntilProgress(t, m, job.ID)
	if err := m.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.exe.part")); err == nil {
		t.Fatal("cancel must delete the partial file")
	}
	for _, j := range m.List() {
		if j.ID == job.ID {
			t.Fatal("cancelled job should leave the queue")
		}
	}
}

func TestDownloadPauseThenResume(t *testing.T) {
	body := bytes.Repeat([]byte("pause-resume-"), 8192)
	srv := slowAssetServer(t, body)
	defer srv.Close()
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	m.SetHTTPClient(testClient(srv))

	sum := sha256.Sum256(body)
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset: catalog.Asset{
			Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL,
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
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
	if _, err := m.Resume(job.ID); err != nil {
		t.Fatal(err)
	}
	final := waitForStatus(t, m, job.ID, "done")
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, body) {
		t.Fatalf("resumed file mismatch: %d vs %d bytes", len(data), len(body))
	}
	if snap := findJob(t, m, job.ID); !snap.Verified {
		t.Fatal("digest not verified after resume")
	}
}

func TestDownloadStaleRangeRestarts(t *testing.T) {
	body := bytes.Repeat([]byte("stale-"), 300)
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			hits++
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	part := filepath.Join(dir, "app.exe.part")
	if err := os.WriteFile(part, bytes.Repeat([]byte("x"), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(dir, nil, func() string { return "" }, nil)
	m.SetHTTPClient(testClient(srv))
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset:   catalog.Asset{Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, m, job.ID, "failed")
	if hits != 1 {
		t.Fatalf("expected one Range probe, got %d", hits)
	}
	if _, err := os.Stat(part); err == nil {
		t.Fatal("stale part file should be cleared")
	}
}

func TestDownloadRejectsBadDigest(t *testing.T) {
	body := []byte("tampered payload")
	srv, _ := startAssetServer(t, body)
	defer srv.Close()
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	m.SetHTTPClient(testClient(srv))
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset: catalog.Asset{
			Name: "app.exe", Size: int64(len(body)), DownloadURL: testAssetURL,
			Digest: "sha256:" + strings.Repeat("a", 64),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, m, job.ID, "failed")
	snap := findJob(t, m, job.ID)
	if !strings.Contains(snap.Error, "校验") {
		t.Fatalf("error = %q, want digest complaint", snap.Error)
	}
	if _, err := os.Stat(snap.DestPath); err == nil {
		t.Fatal("failed download must not leave a final file")
	}
}

func TestDownloadMissingAsset(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	m.SetHTTPClient(testClient(srv))
	job, err := m.Start(Request{
		RepoKey: "o/r",
		Asset:   catalog.Asset{Name: "app.exe", Size: 1024, DownloadURL: testAssetURL},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, m, job.ID, "failed")
	snap := findJob(t, m, job.ID)
	if !strings.Contains(snap.Error, "不存在") {
		t.Fatalf("error = %q, want missing-asset message", snap.Error)
	}
}

// --- helpers ---

type rangeLog struct {
	mu     sync.Mutex
	ranges []string
	paths  []string
}

const assetPath = "/o/r/releases/download/v1/app.exe"

func startAssetServer(t *testing.T, body []byte) (*httptest.Server, *rangeLog) {
	t.Helper()
	rec := &rangeLog{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.mu.Unlock()
		if r.URL.Path != assetPath {
			// Mirrors rewrite the path, so anything else is a bad source.
			http.Error(w, "not a mirror asset", http.StatusInternalServerError)
			return
		}
		if rng := r.Header.Get("Range"); rng != "" {
			rec.mu.Lock()
			rec.ranges = append(rec.ranges, rng)
			rec.mu.Unlock()
			start, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"), 10, 64)
			if err != nil || start <= 0 || start >= int64(len(body)) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-int(start)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start:])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	return srv, rec
}

func slowAssetServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != assetPath {
			http.Error(w, "not an asset", http.StatusInternalServerError)
			return
		}
		var start int64
		if rng := r.Header.Get("Range"); rng != "" {
			start, _ = strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"), 10, 64)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
		}
		flusher, _ := w.(http.Flusher)
		const chunk = 4096
		for i := int(start); i < len(body); i += chunk {
			end := i + chunk
			if end > len(body) {
				end = len(body)
			}
			if _, err := w.Write(body[i:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
}

// testClient keeps the real https URL (so ValidDownloadURL stays happy) but
// dials the TLS test server and accepts its self-signed certificate.
func testClient(srv *httptest.Server) *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
		},
	}
	return &http.Client{Transport: tr}
}

func waitUntilProgress(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range m.List() {
			if j.ID == id && j.Status == "downloading" && j.Done > 0 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("download never reported progress")
}

func findJob(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	for _, j := range m.List() {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %s not found", id)
	return Job{}
}

func waitForStatus(t *testing.T, m *Manager, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range m.List() {
			if j.ID != id {
				continue
			}
			if j.Status == want {
				return j.DestPath
			}
			if j.Status == "failed" && want != "failed" {
				t.Fatalf("job failed: %s", j.Error)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s", id, want)
	return ""
}
