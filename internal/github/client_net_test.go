package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"komi-go/internal/ledger"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *int32, *ledger.Store) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c := NewForHost(srv.URL, func() string { return "" })
	st, err := ledger.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c.SetStore(st)
	return c, &hits, st
}

func TestRetriesOn5xx(t *testing.T) {
	var n int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	})
	login, err := c.Login(context.Background())
	if err != nil || login != "octocat" {
		t.Fatalf("login=%q err=%v", login, err)
	}
}

func TestRateLimitRetryAfter(t *testing.T) {
	var n int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate"}`))
			return
		}
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	})
	start := time.Now()
	login, err := c.Login(context.Background())
	if err != nil || login != "octocat" {
		t.Fatalf("login=%q err=%v", login, err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("Retry-After was not honoured")
	}
}

func TestRepoCacheAndForce(t *testing.T) {
	c, hits, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `W/"one"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"one"`)
		_, _ = w.Write([]byte(`{"id":1,"name":"repo","full_name":"owner/repo","stargazers_count":7}`))
	})
	info, meta, err := c.Repo(context.Background(), "owner", "repo")
	if err != nil || info.Stars != 7 || meta.Cached {
		t.Fatalf("first fetch: %+v %+v err=%v", info, meta, err)
	}
	info, meta, err = c.Repo(context.Background(), "owner", "repo")
	if err != nil || !meta.Cached {
		t.Fatalf("second fetch should be cached: %+v %+v err=%v", info, meta, err)
	}
	before := atomic.LoadInt32(hits)
	if _, _, err := c.RepoDetail(context.Background(), "owner", "repo"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(hits) == before {
		t.Fatal("force refresh did not hit the network")
	}
}

func TestRepoNegativeCache(t *testing.T) {
	c, hits, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	if _, _, err := c.Repo(context.Background(), "owner", "missing"); !IsNotFound(err) {
		t.Fatalf("expected 404, got %v", err)
	}
	before := atomic.LoadInt32(hits)
	if _, _, err := c.Repo(context.Background(), "owner", "missing"); !IsNotFound(err) {
		t.Fatalf("expected cached 404, got %v", err)
	}
	if atomic.LoadInt32(hits) != before {
		t.Fatal("negative cache did not suppress the second request")
	}
}

func TestSingleFlight(t *testing.T) {
	c, hits, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"id":1,"name":"repo","full_name":"owner/repo"}`))
	})
	var done int32
	for i := 0; i < 8; i++ {
		go func() {
			_, _, _ = c.Repo(context.Background(), "owner", "repo")
			atomic.AddInt32(&done, 1)
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&done) < 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("single-flight issued %d requests", got)
	}
}

func TestReadmeRaw(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); !strings.Contains(got, "raw") {
			t.Errorf("Accept=%q", got)
		}
		_, _ = w.Write([]byte("# Hello\n"))
	})
	md, _, err := c.Readme(context.Background(), "owner", "repo")
	if err != nil || md != "# Hello\n" {
		t.Fatalf("readme=%q err=%v", md, err)
	}
}

func TestParseRefHostAndTag(t *testing.T) {
	ref, err := ParseRef("https://codeberg.org/alice/app/releases/tag/v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Host != "codeberg.org" || ref.Owner != "alice" || ref.Name != "app" || ref.Tag != "v1.2.3" {
		t.Fatalf("bad ref: %+v", ref)
	}
	if ref.RepoKeyString() != "codeberg.org/alice/app" {
		t.Fatalf("key=%q", ref.RepoKeyString())
	}
	ref, err = ParseRef("komi://open?url=" + "https%3A%2F%2Fgithub.com%2Fo%2Fr%2Freleases%2Ftag%2Fv2.0")
	if err != nil || ref.Owner != "o" || ref.Name != "r" || ref.Tag != "v2.0" {
		t.Fatalf("deep link ref=%+v err=%v", ref, err)
	}
	if _, err := ParseRef("https://127.0.0.1/o/r"); err == nil {
		t.Fatal("private host accepted")
	}
}

func TestSplitRepoKey(t *testing.T) {
	if host, o, r := SplitRepoKey("owner/repo#auth"); host != "" || o != "owner" || r != "repo" {
		t.Fatalf("split github = %q %q %q", host, o, r)
	}
	if host, o, r := SplitRepoKey("codeberg.org/alice/app"); host != "codeberg.org" || o != "alice" || r != "app" {
		t.Fatalf("split forgejo = %q %q %q", host, o, r)
	}
}
