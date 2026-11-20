package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func backendFixture(t *testing.T) *Backend {
	t.Helper()
	repos := make([]map[string]any, 0, 25)
	for i := 0; i < 25; i++ {
		repos = append(repos, map[string]any{
			"id": i + 1, "name": "repo" + string(rune('a'+i)), "fullName": "owner/repo",
			"owner":       map[string]any{"login": "owner", "avatarUrl": "https://avatar"},
			"description": "demo", "htmlUrl": "https://github.com/owner/repo",
			"stargazersCount": 100 + i, "forksCount": 5, "language": "Go",
			"topics": []string{"tool"}, "topicCodes": []string{"dev"},
			"updatedAt": "2026-11-19 01:51:38+00", "latestReleaseDate": "2026-10-09T15:43:25Z",
			"latestReleaseTag": "v1.2.3", "downloadCount": 42, "dailyStars": 3,
			"trendingScore": 12.5, "popularityScore": 99,
			"hasInstallersWindows": true,
			"platformReleases":     map[string]any{"windows": map[string]any{"tag": "v1.2.3", "publishedAt": "2026-10-09T15:43:25Z"}},
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/categories/trending/windows", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(repos)
	})
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": repos[:20], "page": 1, "hasMore": true, "rotation": "2026-11-20"})
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" || r.URL.Query().Get("sort") == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"bad"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": repos[:2], "totalHits": 2, "source": "postgres"})
	})
	mux.HandleFunc("/repo/owner/demo", func(w http.ResponseWriter, r *http.Request) {
		info := repos[0]
		info["hasInstallersWindows"] = true
		_ = json.NewEncoder(w).Encode(info)
	})
	mux.HandleFunc("/releases/owner/demo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 5, "tag_name": "v1.2.3", "name": "Release", "draft": false, "prerelease": false,
			"published_at": "2026-10-09T15:43:25Z",
			"assets":       []map[string]any{{"id": 9, "name": "app-windows-amd64.exe", "size": 1234, "browser_download_url": "https://github.com/x", "digest": "sha256:abc"}},
		}})
	})
	mux.HandleFunc("/readme/owner/demo", func(w http.ResponseWriter, r *http.Request) {
		content := base64.StdEncoding.EncodeToString([]byte("# Demo\n\nhello"))
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "README.md", "encoding": "base64", "content": content})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	b := NewBackend(func() string { return "tok" })
	b.base = srv.URL
	return b
}

func TestBackendDiscoverPaginatesAndFilters(t *testing.T) {
	b := backendFixture(t)
	ctx := context.Background()

	page1, _, err := b.Discover(ctx, "trending", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 20 {
		t.Fatalf("page1 len=%d", len(page1))
	}
	if page1[0].LatestReleaseTag != "v1.2.3" || page1[0].Language != "Go" || page1[0].DownloadCount != 42 {
		t.Fatalf("enrichment lost: %+v", page1[0])
	}
	page2, _, err := b.Discover(ctx, "trending", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2 len=%d", len(page2))
	}
	ready, _, err := b.Discover(ctx, "trending", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ready {
		if !r.PlatformReady {
			t.Fatalf("onlyInstallable leaked %s", r.Name)
		}
	}
}

func TestBackendFeed(t *testing.T) {
	b := backendFixture(t)
	list, _, err := b.Discover(context.Background(), "windows", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 20 {
		t.Fatalf("feed len=%d", len(list))
	}
}

func TestBackendSearchAndFallbackClassification(t *testing.T) {
	b := backendFixture(t)
	list, total, _, err := b.Search(context.Background(), "demo", 1, 20, "stars", "", false)
	if err != nil || len(list) != 2 || total != 2 {
		t.Fatalf("search: %v len=%d total=%d", err, len(list), total)
	}
	_, _, _, err = b.Search(context.Background(), "demo", 1, 20, "bad", "", false)
	if err == nil {
		t.Fatal("expected 400")
	}
	if BackendShouldFallback(err) {
		t.Fatal("400 must not fall back")
	}
	if !BackendShouldFallback(&Error{Status: 503}) {
		t.Fatal("503 must fall back")
	}
	if !BackendShouldFallback(&Error{Status: 404}) {
		t.Fatal("404 must fall back")
	}
	if BackendShouldFallback(&Error{Status: 429}) {
		t.Fatal("429 must not fall back")
	}
}

func TestBackendRepoReleasesReadme(t *testing.T) {
	b := backendFixture(t)
	info, _, err := b.Repo(context.Background(), "owner", "demo")
	if err != nil || !info.PlatformReady {
		t.Fatalf("repo: %v %+v", err, info)
	}
	if info.LatestReleaseAt.IsZero() {
		t.Fatal("latest release time not parsed")
	}
	rels, _, err := b.Releases(context.Background(), "owner", "demo", 30, false)
	if err != nil || len(rels) != 1 || len(rels[0].Assets) != 1 {
		t.Fatalf("releases: %v %+v", err, rels)
	}
	if rels[0].Assets[0].Digest != "sha256:abc" {
		t.Fatalf("asset digest lost: %+v", rels[0].Assets[0])
	}
	readme, _, err := b.Readme(context.Background(), "owner", "demo")
	if err != nil || readme != "# Demo\n\nhello" {
		t.Fatalf("readme: %v %q", err, readme)
	}
}

func TestParseBackendTime(t *testing.T) {
	got := parseBackendTime("2026-11-19 01:51:38+00")
	want := time.Date(2026, 11, 19, 1, 51, 38, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}
	if z := parseBackendTime("not a date"); !z.IsZero() {
		t.Fatalf("expected zero, got %s", z)
	}
}
