package ledger

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"komi-go/internal/catalog"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "komi.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestInstallLifecycle(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	app := InstalledApp{
		ID: "owner/repo", Owner: "owner", Repo: "repo", DisplayName: "Repo",
		InstalledVersion: "1.0.0", InstalledReleaseTag: "v1.0.0",
		InstalledAssetID: 11, InstalledAssetName: "Repo-1.0.0.exe",
		InstalledAssetDigest: "sha256:aaa", InstalledAssetSize: 52428800, InstalledReleasePubAt: now - 86400,
		InstallPath:   `C:\Users\me\AppData\Local\KomiGo\apps\owner-repo\v1.0.0`,
		InstallSource: "github", InstalledAt: now, UpdateCheckEnabled: true,
	}
	if err := st.UpsertInstall(ctx, app); err != nil {
		t.Fatalf("UpsertInstall: %v", err)
	}

	got, ok, err := st.GetInstalled(ctx, "owner/repo")
	if err != nil || !ok {
		t.Fatalf("GetInstalled ok=%v err=%v", ok, err)
	}
	if got.InstalledReleaseTag != "v1.0.0" || got.Pinned || got.UpdateAvailable {
		t.Fatalf("unexpected row: %+v", got)
	}
	// The update checker compares against the installed asset size, so it has to
	// survive the round trip instead of reading back as zero.
	if got.InstalledAssetSize != 52428800 {
		t.Fatalf("installed_asset_size = %d, want 52428800", got.InstalledAssetSize)
	}

	if err := st.SetPinned(ctx, "owner/repo", true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	got, _, _ = st.GetInstalled(ctx, "owner/repo")
	if !got.Pinned {
		t.Fatal("pinned flag not persisted")
	}

	latest := catalog.Release{TagName: "v1.1.0", PublishedAt: time.Unix(now, 0)}
	asset := catalog.Asset{ID: 12, Name: "Repo-1.1.0.exe", Digest: "sha256:bbb"}
	if err := st.UpdateCheckResult(ctx, "owner/repo", latest, &asset, true, "newer version"); err != nil {
		t.Fatalf("UpdateCheckResult: %v", err)
	}
	got, _, _ = st.GetInstalled(ctx, "owner/repo")
	if !got.UpdateAvailable || got.LatestReleaseTag != "v1.1.0" || got.LatestAssetID != 12 {
		t.Fatalf("update fields wrong: %+v", got)
	}
	if got.LastCheckedAt == 0 || got.LastCheckErr != "" {
		t.Fatalf("check timestamp/error wrong: %+v", got)
	}

	if err := st.SkipVersion(ctx, "owner/repo", "v1.1.0", now); err != nil {
		t.Fatalf("SkipVersion: %v", err)
	}
	got, _, _ = st.GetInstalled(ctx, "owner/repo")
	if got.SkippedReleaseTag != "v1.1.0" || got.SkippedPubAt != now {
		t.Fatalf("skip fields wrong: %+v", got)
	}

	if err := st.DeleteInstalled(ctx, "owner/repo"); err != nil {
		t.Fatalf("DeleteInstalled: %v", err)
	}
	if _, ok, _ := st.GetInstalled(ctx, "owner/repo"); ok {
		t.Fatal("row survived delete")
	}
}

func TestTaskAndCacheRoundTrip(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	task := DownloadTask{
		ID: "t1", RepoKey: "owner/repo", AssetName: "App.exe",
		AssetURL: "https://github.com/x/y", FileName: "App.exe",
		DestPath: `C:\tmp\App.exe`, Total: 100, Done: 10,
		Status: StatusDownloading, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertTask(ctx, task); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if err := st.UpdateTaskProgress(ctx, "t1", 55, 100, StatusDownloading, ""); err != nil {
		t.Fatalf("UpdateTaskProgress: %v", err)
	}
	tasks, err := st.ListTasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasks len=%d err=%v", len(tasks), err)
	}
	if tasks[0].Done != 55 || tasks[0].Status != StatusDownloading {
		t.Fatalf("task not updated: %+v", tasks[0])
	}

	releases := []catalog.Release{
		{ID: 1, TagName: "v1.0.0", PublishedAt: time.Unix(now-10, 0),
			Assets: []catalog.Asset{{ID: 9, Name: "App.exe", Size: 10}}},
		{ID: 2, TagName: "v1.1.0", PublishedAt: time.Unix(now, 0)},
	}
	if err := st.SaveReleases(ctx, "owner/repo", releases, `W/"etag-1"`); err != nil {
		t.Fatalf("SaveReleases: %v", err)
	}
	cached, etag, fetchedAt, ok, err := st.LoadReleases(ctx, "owner/repo")
	if err != nil || !ok || len(cached) != 2 {
		t.Fatalf("LoadReleases ok=%v len=%d err=%v", ok, len(cached), err)
	}
	if etag != `W/"etag-1"` || fetchedAt.IsZero() {
		t.Fatalf("etag/time wrong: %q %v", etag, fetchedAt)
	}
	if cached[0].TagName != "v1.0.0" || len(cached[0].Assets) != 1 || cached[0].Assets[0].ID != 9 {
		t.Fatalf("payload did not round-trip: %+v", cached[0])
	}

	if err := st.SetSetting(ctx, "theme", "dark"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	v, err := st.GetSetting(ctx, "theme")
	if err != nil || v != "dark" {
		t.Fatalf("GetSetting = %q, %v", v, err)
	}
}

func TestV2UpgradeFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "komi.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1 + "\nPRAGMA user_version = 1;"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO installed_app (id, owner, repo, display_name, installed_version, installed_at)
VALUES ('owner/repo','owner','repo','Repo','1.0.0', 100)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open upgraded db: %v", err)
	}
	defer st.Close()
	got, ok, err := st.GetInstalled(context.Background(), "owner/repo")
	if err != nil || !ok {
		t.Fatalf("row lost after upgrade ok=%v err=%v", ok, err)
	}
	if got.LibraryStatus != "installed" || !got.FallbackOlder {
		t.Fatalf("new defaults wrong: %+v", got)
	}
	if err := st.UpdateInstallMeta(context.Background(), "owner/repo", `C:\Apps\Repo`, "{guid}", "msiexec /x {guid}", 4096, 12); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.GetInstalled(context.Background(), "owner/repo")
	if got.InstallDir != `C:\Apps\Repo` || got.InstallSize != 4096 || got.InstalledVersionCode != 12 {
		t.Fatalf("meta not persisted: %+v", got)
	}
}

func TestCacheTTLAndEviction(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := st.PutCache(ctx, "repo:o/r", 200, `W/"x"`, "repo", `{"name":"r"}`, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	e, ok, err := st.GetCache(ctx, "repo:o/r")
	if err != nil || !ok || e.Payload != `{"name":"r"}` || e.ETag != `W/"x"` {
		t.Fatalf("cache round trip ok=%v err=%v entry=%+v", ok, err, e)
	}
	if _, err := st.ClearCache(ctx, "repo:"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetCache(ctx, "repo:o/r"); ok {
		t.Fatal("cache entry survived clear")
	}
	if err := st.PutCache(ctx, "big", 200, "", "test", strings.Repeat("x", 200), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.TrimCache(ctx, 100); err != nil {
		t.Fatal(err)
	}
	n, _, _ := st.CacheStats(ctx)
	if n != 0 {
		t.Fatalf("expected eviction, still %d entries", n)
	}
}
