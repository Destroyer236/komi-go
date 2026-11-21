// Package ledger owns the SQLite database: installed apps, download tasks,
// cached releases and settings. Schema is versioned with PRAGMA user_version so
// upgrades never destroy user data.
package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"komi-go/internal/catalog"
)

// Store is the single database handle used by the app.
type Store struct {
	db   *sql.DB
	path string
}

// InstalledApp is the ledger row and the JSON payload the UI renders.
// Fields are deliberately complete from v1: adding them later costs a migration.
type InstalledApp struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	DisplayName string `json:"displayName"`

	InstalledVersion      string `json:"installedVersion"`
	InstalledVersionCode  int64  `json:"installedVersionCode"`
	InstalledAssetName    string `json:"installedAssetName"`
	InstalledAssetID      int64  `json:"installedAssetId"`
	InstalledAssetDigest  string `json:"installedAssetDigest"`
	InstalledAssetURL     string `json:"installedAssetUrl"`
	InstalledAssetSize    int64  `json:"installedAssetSize"`
	InstalledReleaseID    int64  `json:"installedReleaseId"`
	InstalledReleaseTag   string `json:"installedReleaseTag"`
	InstalledReleasePubAt int64  `json:"installedReleasePublishedAt"`

	InstallPath   string `json:"installPath"`
	InstallDir    string `json:"installDir"`
	InstallSize   int64  `json:"installSize"`
	InstallSource string `json:"installSource"`
	ProductCode   string `json:"productCode"`
	UninstallStr  string `json:"uninstallString"`
	LibraryStatus string `json:"libraryStatus"` // installed | tracked | uninstalled
	UninstalledAt int64  `json:"uninstalledAt"`
	AppSlug       string `json:"appSlug"`
	SourceHost    string `json:"sourceHost"`

	LatestVersion      string `json:"latestVersion"`
	LatestVersionCode  int64  `json:"latestVersionCode"`
	LatestAssetName    string `json:"latestAssetName"`
	LatestAssetID      int64  `json:"latestAssetId"`
	LatestAssetDigest  string `json:"latestAssetDigest"`
	LatestAssetURL     string `json:"latestAssetUrl"`
	LatestAssetSize    int64  `json:"latestAssetSize"`
	LatestReleaseID    int64  `json:"latestReleaseId"`
	LatestReleaseTag   string `json:"latestReleaseTag"`
	LatestReleasePubAt int64  `json:"latestReleasePublishedAt"`

	UpdateAvailable    bool `json:"updateAvailable"`
	Pinned             bool `json:"pinned"`
	UpdateCheckEnabled bool `json:"updateCheckEnabled"`

	// Selection preferences keep the next update in the same asset family.
	AssetGlob         string `json:"assetGlob"`
	PreferredVariant  string `json:"preferredVariant"`
	PickedIndex       int    `json:"pickedIndex"`
	SiblingCount      int    `json:"siblingCount"`
	IncludePrerelease bool   `json:"includePrereleases"`
	FallbackOlder     bool   `json:"fallbackToOlderReleases"`
	AssetFilter       string `json:"assetFilterRegex"`

	SkippedReleaseTag string `json:"skippedReleaseTag"`
	SkippedPubAt      int64  `json:"skippedPublishedAt"`

	PendingInstallPath string `json:"pendingInstallPath"`
	PendingVersion     string `json:"pendingVersion"`
	PendingAssetName   string `json:"pendingAssetName"`
	PendingReleaseTag  string `json:"pendingReleaseTag"`
	PendingAssetID     int64  `json:"pendingAssetId"`
	PendingDigest      string `json:"pendingDigest"`

	InstalledAt   int64  `json:"installedAt"`
	LastCheckedAt int64  `json:"lastCheckedAt"`
	LastCheckErr  string `json:"lastCheckError"`
}

// DownloadTask is the persisted download queue row.
type DownloadTask struct {
	ID          string `json:"id"`
	RepoKey     string `json:"repoKey"`
	DisplayName string `json:"displayName"`
	AssetName   string `json:"assetName"`
	AssetID     int64  `json:"assetId"`
	AssetURL    string `json:"assetUrl"`
	FileName    string `json:"fileName"`
	DestPath    string `json:"destPath"`
	Total       int64  `json:"total"`
	Done        int64  `json:"done"`
	Status      string `json:"status"`
	Err         string `json:"error"`
	ETag        string `json:"etag"`
	Digest      string `json:"digest"`
	Tag         string `json:"tag"`
	Version     string `json:"version"`
	PublishedAt int64  `json:"publishedAt"`
	Verified    bool   `json:"verified"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// Download states shared with the downloader and the UI.
const (
	StatusQueued      = "queued"
	StatusDownloading = "downloading"
	StatusVerifying   = "verifying"
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusPaused      = "paused"
	StatusCanceled    = "canceled"
	StatusInstalling  = "installing"
	StatusUnverified  = "unverified"
)

// Open creates or opens the database and runs migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close flushes WAL and closes the handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS installed_app (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  display_name TEXT NOT NULL,

  installed_version TEXT,
  installed_version_code INTEGER NOT NULL DEFAULT 0,
  installed_asset_name TEXT,
  installed_asset_id INTEGER NOT NULL DEFAULT 0,
  installed_asset_digest TEXT,
  installed_asset_url TEXT,
  installed_release_id INTEGER NOT NULL DEFAULT 0,
  installed_release_tag TEXT,
  installed_release_published_at INTEGER NOT NULL DEFAULT 0,

  install_path TEXT,
  install_source TEXT,

  latest_version TEXT,
  latest_version_code INTEGER NOT NULL DEFAULT 0,
  latest_asset_name TEXT,
  latest_asset_id INTEGER NOT NULL DEFAULT 0,
  latest_asset_digest TEXT,
  latest_release_tag TEXT,
  latest_release_published_at INTEGER NOT NULL DEFAULT 0,

  update_available INTEGER NOT NULL DEFAULT 0,
  pinned INTEGER NOT NULL DEFAULT 0,
  update_check_enabled INTEGER NOT NULL DEFAULT 1,

  skipped_release_tag TEXT,
  skipped_published_at INTEGER NOT NULL DEFAULT 0,

  pending_install_path TEXT,
  pending_version TEXT,
  pending_asset_name TEXT,
  pending_release_tag TEXT,
  pending_asset_id INTEGER NOT NULL DEFAULT 0,
  pending_digest TEXT,

  installed_at INTEGER NOT NULL DEFAULT 0,
  last_checked_at INTEGER NOT NULL DEFAULT 0,
  last_check_error TEXT
);
CREATE INDEX IF NOT EXISTS idx_installed_repo ON installed_app(owner, repo);
CREATE INDEX IF NOT EXISTS idx_installed_update ON installed_app(update_available);

CREATE TABLE IF NOT EXISTS download_task (
  id TEXT PRIMARY KEY,
  repo_key TEXT NOT NULL DEFAULT '',
  asset_name TEXT NOT NULL DEFAULT '',
  asset_url TEXT NOT NULL,
  file_name TEXT NOT NULL,
  dest_path TEXT NOT NULL DEFAULT '',
  total_bytes INTEGER NOT NULL DEFAULT 0,
  done_bytes INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  etag TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS release_cache (
  repo_key TEXT NOT NULL,
  tag_name TEXT NOT NULL,
  payload TEXT NOT NULL,
  etag TEXT NOT NULL DEFAULT '',
  fetched_at INTEGER NOT NULL,
  PRIMARY KEY (repo_key, tag_name)
);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// schemaV2 fields are added by migrateV2 through columnSpec so a partially
// applied upgrade is safe to re-run.

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 1 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, schemaV1); err != nil {
			tx.Rollback()
			return fmt.Errorf("初始化数据库失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		version = 1
	}
	if version < 2 {
		if err := s.migrateV2(ctx); err != nil {
			return fmt.Errorf("升级数据库到 v2 失败: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
			return err
		}
		version = 2
	}
	if version < 3 {
		// Older Komi builds used shorter column names (installed_digest,
		// installed_tag, ...). Add the v2 columns that a pre-upgrade database may
		// still be missing, then alias the legacy values.
		if err := s.migrateV2(ctx); err != nil {
			return fmt.Errorf("升级数据库到 v3 失败: %w", err)
		}
		for _, c := range v3Columns {
			exists, err := s.hasColumn(ctx, c.table, c.name)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			if _, err := s.db.ExecContext(ctx,
				"ALTER TABLE "+c.table+" ADD COLUMN "+c.name+" "+c.ddl); err != nil {
				return err
			}
		}
		type backfillSpec struct{ legacy, stmt string }
		backfills := []backfillSpec{
			{"installed_digest", `UPDATE installed_app SET installed_asset_digest=COALESCE(NULLIF(installed_asset_digest,''),installed_digest,'')`},
			{"installed_tag", `UPDATE installed_app SET installed_release_tag=COALESCE(NULLIF(installed_release_tag,''),installed_tag,'')`},
			{"latest_digest", `UPDATE installed_app SET latest_asset_digest=COALESCE(NULLIF(latest_asset_digest,''),latest_digest,'')`},
			{"latest_tag", `UPDATE installed_app SET latest_release_tag=COALESCE(NULLIF(latest_release_tag,''),latest_tag,'')`},
			{"skipped_tag", `UPDATE installed_app SET skipped_release_tag=COALESCE(NULLIF(skipped_release_tag,''),skipped_tag,'')`},
			{"pending_file_path", `UPDATE installed_app SET pending_install_path=COALESCE(NULLIF(pending_install_path,''),pending_file_path,'')`},
			{"install_location", `UPDATE installed_app SET install_path=COALESCE(NULLIF(install_path,''),install_location,'')`},
		}
		for _, b := range backfills {
			exists, err := s.hasColumn(ctx, "installed_app", b.legacy)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			if _, err := s.db.ExecContext(ctx, b.stmt); err != nil {
				return fmt.Errorf("回填旧数据库字段失败: %w", err)
			}
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 3"); err != nil {
			return err
		}
	}
	return nil
}

type columnSpec struct {
	table string
	name  string
	ddl   string
}

var v2Columns = []columnSpec{
	{"installed_app", "install_dir", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "install_size", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "product_code", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "uninstall_string", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "library_status", "TEXT NOT NULL DEFAULT 'installed'"},
	{"installed_app", "uninstalled_at", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "app_slug", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "source_host", "TEXT NOT NULL DEFAULT 'github.com'"},
	{"installed_app", "latest_asset_url", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "latest_asset_size", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "latest_release_id", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "asset_glob", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "preferred_variant", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "picked_index", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "sibling_count", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "include_prerelease", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "fallback_older", "INTEGER NOT NULL DEFAULT 1"},
	{"installed_app", "asset_filter", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "installed_asset_size", "INTEGER NOT NULL DEFAULT 0"},
	{"download_task", "display_name", "TEXT NOT NULL DEFAULT ''"},
	{"download_task", "asset_id", "INTEGER NOT NULL DEFAULT 0"},
	{"download_task", "digest", "TEXT NOT NULL DEFAULT ''"},
	{"download_task", "tag_value", "TEXT NOT NULL DEFAULT ''"},
	{"download_task", "version", "TEXT NOT NULL DEFAULT ''"},
	{"download_task", "published_at", "INTEGER NOT NULL DEFAULT 0"},
	{"download_task", "verified", "INTEGER NOT NULL DEFAULT 0"},
}

const schemaV2Tables = `
CREATE TABLE IF NOT EXISTS http_cache (
  key TEXT PRIMARY KEY,
  payload TEXT NOT NULL DEFAULT '',
  etag TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL DEFAULT '',
  fetched_at INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS installable_cache (
  repo_key TEXT NOT NULL,
  target TEXT NOT NULL,
  ready INTEGER NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT '',
  checked_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (repo_key, target)
);
CREATE INDEX IF NOT EXISTS idx_library_status ON installed_app(library_status);
`

var v3Columns = []columnSpec{
	{"installed_app", "installed_asset_digest", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "installed_release_tag", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "installed_release_published_at", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "latest_asset_digest", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "latest_release_tag", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "latest_release_published_at", "INTEGER NOT NULL DEFAULT 0"},
	{"installed_app", "skipped_release_tag", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "pending_install_path", "TEXT NOT NULL DEFAULT ''"},
	{"installed_app", "pending_release_tag", "TEXT NOT NULL DEFAULT ''"},
}

// migrateV2 is idempotent: a crashed upgrade can simply run again.
func (s *Store) migrateV2(ctx context.Context) error {
	for _, c := range v2Columns {
		exists, err := s.hasColumn(ctx, c.table, c.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			"ALTER TABLE "+c.table+" ADD COLUMN "+c.name+" "+c.ddl); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, schemaV2Tables)
	return err
}

func (s *Store) hasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ---------------------------------------------------------------------------
// installed_app
// ---------------------------------------------------------------------------

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpsertInstall writes the full installed snapshot, preserving pending and
// latest fields that the caller does not own.
func (s *Store) UpsertInstall(ctx context.Context, a InstalledApp) error {
	if a.ID == "" {
		return errors.New("installed app id 为空")
	}
	if a.InstalledAt == 0 {
		a.InstalledAt = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO installed_app (
  id, owner, repo, display_name,
  installed_version, installed_version_code, installed_asset_name, installed_asset_id,
  installed_asset_digest, installed_asset_url, installed_asset_size,
  installed_release_id, installed_release_tag,
  installed_release_published_at, install_path, install_dir, install_size,
  install_source, product_code, uninstall_string, library_status, uninstalled_at,
  app_slug, source_host,
  update_available, pinned, update_check_enabled,
  asset_glob, preferred_variant, picked_index, sibling_count,
  include_prerelease, fallback_older, asset_filter,
  skipped_release_tag, skipped_published_at,
  pending_install_path, pending_version, pending_asset_name, pending_release_tag,
  pending_asset_id, pending_digest,
  installed_at, last_checked_at, last_check_error
) VALUES (?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  owner=excluded.owner, repo=excluded.repo, display_name=excluded.display_name,
  installed_version=excluded.installed_version,
  installed_version_code=excluded.installed_version_code,
  installed_asset_name=excluded.installed_asset_name,
  installed_asset_id=excluded.installed_asset_id,
  installed_asset_digest=excluded.installed_asset_digest,
  installed_asset_url=excluded.installed_asset_url,
  installed_asset_size=excluded.installed_asset_size,
  installed_release_id=excluded.installed_release_id,
  installed_release_tag=excluded.installed_release_tag,
  installed_release_published_at=excluded.installed_release_published_at,
  install_path=excluded.install_path, install_dir=excluded.install_dir,
  install_size=excluded.install_size, install_source=excluded.install_source,
  product_code=excluded.product_code, uninstall_string=excluded.uninstall_string,
  library_status=excluded.library_status, uninstalled_at=excluded.uninstalled_at,
  app_slug=excluded.app_slug, source_host=excluded.source_host,
  update_available=excluded.update_available, pinned=excluded.pinned,
  update_check_enabled=excluded.update_check_enabled,
  asset_glob=excluded.asset_glob, preferred_variant=excluded.preferred_variant,
  picked_index=excluded.picked_index, sibling_count=excluded.sibling_count,
  include_prerelease=excluded.include_prerelease,
  fallback_older=excluded.fallback_older, asset_filter=excluded.asset_filter,
  skipped_release_tag=excluded.skipped_release_tag,
  skipped_published_at=excluded.skipped_published_at,
  pending_install_path=excluded.pending_install_path,
  pending_version=excluded.pending_version,
  pending_asset_name=excluded.pending_asset_name,
  pending_release_tag=excluded.pending_release_tag,
  pending_asset_id=excluded.pending_asset_id,
  pending_digest=excluded.pending_digest,
  installed_at=excluded.installed_at,
  last_checked_at=excluded.last_checked_at,
  last_check_error=excluded.last_check_error`,
		a.ID, a.Owner, a.Repo, a.DisplayName,
		a.InstalledVersion, a.InstalledVersionCode, a.InstalledAssetName, a.InstalledAssetID,
		a.InstalledAssetDigest, a.InstalledAssetURL, a.InstalledAssetSize, a.InstalledReleaseID, a.InstalledReleaseTag,
		a.InstalledReleasePubAt, a.InstallPath, a.InstallDir, a.InstallSize,
		a.InstallSource, a.ProductCode, a.UninstallStr, a.LibraryStatus, a.UninstalledAt,
		a.AppSlug, a.SourceHost,
		boolInt(a.UpdateAvailable), boolInt(a.Pinned), boolInt(a.UpdateCheckEnabled),
		a.AssetGlob, a.PreferredVariant, a.PickedIndex, a.SiblingCount,
		boolInt(a.IncludePrerelease), boolInt(a.FallbackOlder), a.AssetFilter,
		a.SkippedReleaseTag, a.SkippedPubAt,
		a.PendingInstallPath, a.PendingVersion, a.PendingAssetName, a.PendingReleaseTag,
		a.PendingAssetID, a.PendingDigest,
		a.InstalledAt, a.LastCheckedAt, a.LastCheckErr,
	)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanApp(row scanner) (InstalledApp, error) {
	var a InstalledApp
	var updateAvailable, pinned, checkEnabled, includePre, fallbackOlder int
	err := row.Scan(
		&a.ID, &a.Owner, &a.Repo, &a.DisplayName,
		&a.InstalledVersion, &a.InstalledVersionCode, &a.InstalledAssetName, &a.InstalledAssetID,
		&a.InstalledAssetDigest, &a.InstalledAssetURL, &a.InstalledAssetSize, &a.InstalledReleaseID, &a.InstalledReleaseTag,
		&a.InstalledReleasePubAt,
		&a.InstallPath, &a.InstallDir, &a.InstallSize,
		&a.InstallSource, &a.ProductCode, &a.UninstallStr, &a.LibraryStatus, &a.UninstalledAt,
		&a.AppSlug, &a.SourceHost,
		&a.LatestVersion, &a.LatestVersionCode, &a.LatestAssetName, &a.LatestAssetID,
		&a.LatestAssetDigest, &a.LatestAssetURL, &a.LatestAssetSize, &a.LatestReleaseID,
		&a.LatestReleaseTag, &a.LatestReleasePubAt,
		&updateAvailable, &pinned, &checkEnabled,
		&a.AssetGlob, &a.PreferredVariant, &a.PickedIndex, &a.SiblingCount,
		&includePre, &fallbackOlder, &a.AssetFilter,
		&a.SkippedReleaseTag, &a.SkippedPubAt,
		&a.PendingInstallPath, &a.PendingVersion, &a.PendingAssetName, &a.PendingReleaseTag,
		&a.PendingAssetID, &a.PendingDigest,
		&a.InstalledAt, &a.LastCheckedAt, &a.LastCheckErr,
	)
	if err != nil {
		return a, err
	}
	a.UpdateAvailable = updateAvailable != 0
	a.Pinned = pinned != 0
	a.UpdateCheckEnabled = checkEnabled != 0
	a.IncludePrerelease = includePre != 0
	a.FallbackOlder = fallbackOlder != 0
	return a, nil
}

// COALESCE keeps rows written before a column existed readable: SQLite returns
// NULL for omitted columns, and database/sql cannot scan NULL into string/int.
const installedColumns = `
  id, owner, repo, display_name,
  COALESCE(installed_version,''), COALESCE(installed_version_code,0),
  COALESCE(installed_asset_name,''), COALESCE(installed_asset_id,0),
  COALESCE(installed_asset_digest,''), COALESCE(installed_asset_url,''),
  COALESCE(installed_asset_size,0),
  COALESCE(installed_release_id,0), COALESCE(installed_release_tag,''),
  COALESCE(installed_release_published_at,0),
  COALESCE(install_path,''), COALESCE(install_dir,''), COALESCE(install_size,0),
  COALESCE(install_source,''), COALESCE(product_code,''), COALESCE(uninstall_string,''),
  COALESCE(library_status,'installed'), COALESCE(uninstalled_at,0),
  COALESCE(app_slug,''), COALESCE(source_host,'github.com'),
  COALESCE(latest_version,''), COALESCE(latest_version_code,0),
  COALESCE(latest_asset_name,''), COALESCE(latest_asset_id,0),
  COALESCE(latest_asset_digest,''), COALESCE(latest_asset_url,''),
  COALESCE(latest_asset_size,0), COALESCE(latest_release_id,0),
  COALESCE(latest_release_tag,''), COALESCE(latest_release_published_at,0),
  COALESCE(update_available,0), COALESCE(pinned,0), COALESCE(update_check_enabled,0),
  COALESCE(asset_glob,''), COALESCE(preferred_variant,''), COALESCE(picked_index,0),
  COALESCE(sibling_count,0), COALESCE(include_prerelease,0), COALESCE(fallback_older,1),
  COALESCE(asset_filter,''),
  COALESCE(skipped_release_tag,''), CAST(COALESCE(skipped_published_at,0) AS INTEGER),
  COALESCE(pending_install_path,''), COALESCE(pending_version,''),
  COALESCE(pending_asset_name,''), COALESCE(pending_release_tag,''),
  COALESCE(pending_asset_id,0), COALESCE(pending_digest,''),
  COALESCE(installed_at,0), COALESCE(last_checked_at,0), COALESCE(last_check_error,'')`

// GetInstalled returns one app or (zero,false,nil).
func (s *Store) GetInstalled(ctx context.Context, id string) (InstalledApp, bool, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+installedColumns+" FROM installed_app WHERE id = ?", id)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return InstalledApp{}, false, nil
	}
	if err != nil {
		return InstalledApp{}, false, err
	}
	return a, true, nil
}

// ListInstalled returns every tracked app, newest first.
func (s *Store) ListInstalled(ctx context.Context) ([]InstalledApp, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+installedColumns+" FROM installed_app ORDER BY installed_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstalledApp
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateCheckResult records a successful or failed update probe. A failure
// keeps the previous verdict: never clear a badge because the network blipped.
func (s *Store) UpdateCheckResult(ctx context.Context, id string, latest catalog.Release, asset *catalog.Asset, update bool, reason string) error {
	now := time.Now().Unix()
	if asset == nil {
		_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET last_checked_at = ?, last_check_error = '' WHERE id = ?`, now, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET
  latest_version = ?, latest_version_code = 0,
  latest_asset_name = ?, latest_asset_id = ?, latest_asset_digest = ?,
  latest_asset_url = ?, latest_asset_size = ?, latest_release_id = ?,
  latest_release_tag = ?, latest_release_published_at = ?,
  update_available = ?, last_checked_at = ?, last_check_error = ''
WHERE id = ?`,
		latest.TagName, asset.Name, asset.ID, asset.Digest,
		asset.DownloadURL, asset.Size, latest.ID,
		latest.TagName, latest.PublishedAt.Unix(),
		boolInt(update), now, id,
	)
	return err
}

// SetCheckError stores the failure text without touching the update badge.
func (s *Store) SetCheckError(ctx context.Context, id string, msg string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE installed_app SET last_checked_at = ?, last_check_error = ? WHERE id = ?",
		time.Now().Unix(), msg, id)
	return err
}

// SetPending records a downloaded-but-not-installed file.
func (s *Store) SetPending(ctx context.Context, id, path, version, assetName, tag, digest string, assetID int64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET
  pending_install_path = ?, pending_version = ?, pending_asset_name = ?,
  pending_release_tag = ?, pending_asset_id = ?, pending_digest = ?
WHERE id = ?`,
		path, version, assetName, tag, assetID, digest, id)
	return err
}

// ClearPending removes the pending marker after install or cancel.
func (s *Store) ClearPending(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET
  pending_install_path = '', pending_version = '', pending_asset_name = '',
  pending_release_tag = '', pending_asset_id = 0, pending_digest = ''
WHERE id = ?`, id)
	return err
}

// UpdateInstallMeta records what the system installer actually produced.
func (s *Store) UpdateInstallMeta(ctx context.Context, id, dir, productCode, uninstallStr string, size int64, versionCode int64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET
  install_dir = ?, install_size = ?, product_code = ?, uninstall_string = ?,
  installed_version_code = CASE WHEN ? > 0 THEN ? ELSE installed_version_code END
WHERE id = ?`, dir, size, productCode, uninstallStr, versionCode, versionCode, id)
	return err
}

// MarkUninstalled keeps the row for 7 days so the user can re-track it.
func (s *Store) MarkUninstalled(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET
  library_status = 'uninstalled', uninstalled_at = ?, update_available = 0
WHERE id = ?`, time.Now().Unix(), id)
	return err
}

// RestoreTracking puts an uninstalled row back into the library.
func (s *Store) RestoreTracking(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET library_status = 'tracked', uninstalled_at = 0
WHERE id = ?`, id)
	return err
}

// PurgeUninstalled removes rows uninstalled longer than the retention window.
func (s *Store) PurgeUninstalled(ctx context.Context, olderThan time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM installed_app WHERE library_status = 'uninstalled' AND uninstalled_at > 0 AND uninstalled_at < ?",
		olderThan.Unix())
	return err
}

// SetSelectionPrefs stores the asset family and filters used for the next check.
func (s *Store) SetSelectionPrefs(ctx context.Context, id, glob, variant, filter string, includePre, fallback bool, picked, siblings int) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET
  asset_glob = ?, preferred_variant = ?, asset_filter = ?,
  include_prerelease = ?, fallback_older = ?, picked_index = ?, sibling_count = ?
WHERE id = ?`,
		glob, variant, filter, boolInt(includePre), boolInt(fallback), picked, siblings, id)
	return err
}

// SetUpdateCheck toggles per-app update checking.
func (s *Store) SetUpdateCheck(ctx context.Context, id string, enabled bool) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE installed_app SET update_check_enabled = ? WHERE id = ?", boolInt(enabled), id)
	return err
}

// SetPinned toggles update suppression.
func (s *Store) SetPinned(ctx context.Context, id string, pinned bool) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE installed_app SET pinned = ?, update_available = CASE WHEN ? = 1 THEN 0 ELSE update_available END WHERE id = ?",
		boolInt(pinned), boolInt(pinned), id)
	return err
}

// SkipVersion hides a tag until it is rebuilt or a newer tag appears.
func (s *Store) SkipVersion(ctx context.Context, id, tag string, publishedAt int64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE installed_app SET skipped_release_tag = ?, skipped_published_at = ?, update_available = 0
WHERE id = ?`, tag, publishedAt, id)
	return err
}

// DeleteInstalled forgets an app entirely.
func (s *Store) DeleteInstalled(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM installed_app WHERE id = ?", id)
	return err
}

// ---------------------------------------------------------------------------
// download_task
// ---------------------------------------------------------------------------

// UpsertTask inserts or updates a download row.
func (s *Store) UpsertTask(ctx context.Context, t DownloadTask) error {
	now := time.Now().Unix()
	if t.CreatedAt == 0 {
		t.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO download_task (
  id, repo_key, display_name, asset_name, asset_id, asset_url,
  file_name, dest_path, total_bytes, done_bytes, status, error, etag,
  digest, tag_value, version, published_at, verified, created_at, updated_at
) VALUES (?,?,?,?,?,?, ?,?,?,?,?,?,?, ?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  repo_key=excluded.repo_key, display_name=excluded.display_name,
  asset_name=excluded.asset_name, asset_id=excluded.asset_id, asset_url=excluded.asset_url,
  file_name=excluded.file_name, dest_path=excluded.dest_path,
  total_bytes=excluded.total_bytes, done_bytes=excluded.done_bytes,
  status=excluded.status, error=excluded.error, etag=excluded.etag,
  digest=excluded.digest, tag_value=excluded.tag_value, version=excluded.version,
  published_at=excluded.published_at, verified=excluded.verified,
  updated_at=excluded.updated_at`,
		t.ID, t.RepoKey, t.DisplayName, t.AssetName, t.AssetID, t.AssetURL,
		t.FileName, t.DestPath, t.Total, t.Done, t.Status, t.Err, t.ETag,
		t.Digest, t.Tag, t.Version, t.PublishedAt, boolInt(t.Verified), t.CreatedAt, now,
	)
	return err
}

// UpdateTaskProgress rewrites the hot columns only.
func (s *Store) UpdateTaskProgress(ctx context.Context, id string, done, total int64, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE download_task SET done_bytes = ?, total_bytes = ?, status = ?, error = ?, updated_at = ?
WHERE id = ?`, done, total, status, errMsg, time.Now().Unix(), id)
	return err
}

// SetTaskVerified records whether a published digest actually matched.
func (s *Store) SetTaskVerified(ctx context.Context, id string, verified bool) error {
	_, err := s.db.ExecContext(ctx, "UPDATE download_task SET verified = ? WHERE id = ?", boolInt(verified), id)
	return err
}

// ListTasks returns the persisted queue, newest first.
func (s *Store) ListTasks(ctx context.Context) ([]DownloadTask, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo_key, COALESCE(display_name,''), asset_name, COALESCE(asset_id,0), asset_url,
       file_name, COALESCE(dest_path,''), COALESCE(total_bytes,0), COALESCE(done_bytes,0),
       status, COALESCE(error,''), COALESCE(etag,''), COALESCE(digest,''),
       COALESCE(tag_value,''), COALESCE(version,''), COALESCE(published_at,0),
       COALESCE(verified,0), created_at, updated_at
FROM download_task ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DownloadTask
	for rows.Next() {
		var t DownloadTask
		var verified int
		if err := rows.Scan(&t.ID, &t.RepoKey, &t.DisplayName, &t.AssetName, &t.AssetID, &t.AssetURL,
			&t.FileName, &t.DestPath, &t.Total, &t.Done,
			&t.Status, &t.Err, &t.ETag, &t.Digest,
			&t.Tag, &t.Version, &t.PublishedAt, &verified, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Verified = verified != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTask removes a finished row.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM download_task WHERE id = ?", id)
	return err
}

// ---------------------------------------------------------------------------
// release_cache
// ---------------------------------------------------------------------------

// SaveReleases replaces the cached release window for a repo.
func (s *Store) SaveReleases(ctx context.Context, repoKey string, releases []catalog.Release, etag string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM release_cache WHERE repo_key = ?", repoKey); err != nil {
		tx.Rollback()
		return err
	}
	now := time.Now().Unix()
	for _, r := range releases {
		payload, err := json.Marshal(r)
		if err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO release_cache (repo_key, tag_name, payload, etag, fetched_at)
VALUES (?,?,?,?,?)`, repoKey, r.TagName, string(payload), etag, now); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LoadReleases returns the cached window and the ETag captured with it.
func (s *Store) LoadReleases(ctx context.Context, repoKey string) ([]catalog.Release, string, time.Time, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT payload, etag, fetched_at FROM release_cache WHERE repo_key = ?`, repoKey)
	if err != nil {
		return nil, "", time.Time{}, false, err
	}
	defer rows.Close()
	var out []catalog.Release
	var etag string
	var fetched int64
	for rows.Next() {
		var payload string
		var rowTag string
		var rowFetched int64
		if err := rows.Scan(&payload, &rowTag, &rowFetched); err != nil {
			return nil, "", time.Time{}, false, err
		}
		var r catalog.Release
		if err := json.Unmarshal([]byte(payload), &r); err != nil {
			continue
		}
		out = append(out, r)
		etag = rowTag
		fetched = rowFetched
	}
	if len(out) == 0 {
		return nil, "", time.Time{}, false, nil
	}
	return out, etag, time.Unix(fetched, 0), true, rows.Err()
}

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------

// GetSetting returns "" when absent.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting upserts a value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings (key, value) VALUES (?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// AllSettings returns every persisted key/value pair (used by diagnostics).
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// DeleteSetting removes a key.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
	return err
}

// ---------------------------------------------------------------------------
// http_cache
// ---------------------------------------------------------------------------

// CacheEntry is one cached HTTP payload with its TTL metadata.
type CacheEntry struct {
	Payload   string
	ETag      string
	Status    int
	Kind      string
	FetchedAt time.Time
	ExpiresAt time.Time
}

// GetCache returns the row even when it is expired; callers decide whether to
// revalidate. A stale row is still useful as an offline fallback.
func (s *Store) GetCache(ctx context.Context, key string) (CacheEntry, bool, error) {
	var e CacheEntry
	var fetched, expires int64
	err := s.db.QueryRowContext(ctx, `
SELECT payload, COALESCE(etag,''), COALESCE(status,0), COALESCE(kind,''),
       COALESCE(fetched_at,0), COALESCE(expires_at,0)
FROM http_cache WHERE key = ?`, key).
		Scan(&e.Payload, &e.ETag, &e.Status, &e.Kind, &fetched, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return CacheEntry{}, false, nil
	}
	if err != nil {
		return CacheEntry{}, false, err
	}
	e.FetchedAt = time.Unix(fetched, 0)
	e.ExpiresAt = time.Unix(expires, 0)
	return e, true, nil
}

// PutCache upserts a payload with an absolute TTL.
func (s *Store) PutCache(ctx context.Context, key string, status int, etag, kind, payload string, ttl time.Duration) error {
	now := time.Now()
	expires := now.Add(ttl)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO http_cache (key, payload, etag, status, kind, fetched_at, expires_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(key) DO UPDATE SET
  payload=excluded.payload, etag=excluded.etag, status=excluded.status, kind=excluded.kind,
  fetched_at=excluded.fetched_at, expires_at=excluded.expires_at`,
		key, payload, etag, status, kind, now.Unix(), expires.Unix())
	return err
}

// TouchCache extends a revalidated payload (304) without rewriting the body.
func (s *Store) TouchCache(ctx context.Context, key string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
UPDATE http_cache SET fetched_at = ?, expires_at = ? WHERE key = ?`,
		now.Unix(), now.Add(ttl).Unix(), key)
	return err
}

// DeleteCache removes one key (used for manual refresh and negative cache reset).
func (s *Store) DeleteCache(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM http_cache WHERE key = ?", key)
	return err
}

// ClearCache deletes entries whose key starts with prefix; "" clears everything.
func (s *Store) ClearCache(ctx context.Context, prefix string) (int64, error) {
	var res sql.Result
	var err error
	if prefix == "" {
		res, err = s.db.ExecContext(ctx, "DELETE FROM http_cache")
	} else {
		res, err = s.db.ExecContext(ctx, "DELETE FROM http_cache WHERE key LIKE ?", prefix+"%")
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CacheStats reports entry count and payload bytes for the settings page.
func (s *Store) CacheStats(ctx context.Context) (int, int64, error) {
	var n int
	var bytes int64
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(LENGTH(payload)),0) FROM http_cache").Scan(&n, &bytes)
	return n, bytes, err
}

// TrimCache evicts the oldest rows until the payload total is under maxBytes.
func (s *Store) TrimCache(ctx context.Context, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	_, total, err := s.CacheStats(ctx)
	if err != nil || total <= maxBytes {
		return err
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT key, LENGTH(payload) FROM http_cache ORDER BY fetched_at ASC")
	if err != nil {
		return err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() && total > maxBytes {
		var key string
		var size int64
		if err := rows.Scan(&key, &size); err != nil {
			return err
		}
		keys = append(keys, key)
		total -= size
	}
	for _, key := range keys {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM http_cache WHERE key = ?", key); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// installable_cache
// ---------------------------------------------------------------------------

// GetInstallable returns the cached "has a Windows asset" verdict.
func (s *Store) GetInstallable(ctx context.Context, repoKey, target string) (bool, string, time.Time, bool, error) {
	var ready int
	var detail string
	var checked int64
	err := s.db.QueryRowContext(ctx, `
SELECT ready, COALESCE(detail,''), COALESCE(checked_at,0)
FROM installable_cache WHERE repo_key = ? AND target = ?`, repoKey, target).
		Scan(&ready, &detail, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", time.Time{}, false, nil
	}
	if err != nil {
		return false, "", time.Time{}, false, err
	}
	return ready != 0, detail, time.Unix(checked, 0), true, nil
}

// PutInstallable stores a verdict with a 24h TTL by convention.
func (s *Store) PutInstallable(ctx context.Context, repoKey, target string, ready bool, detail string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO installable_cache (repo_key, target, ready, detail, checked_at)
VALUES (?,?,?,?,?)
ON CONFLICT(repo_key, target) DO UPDATE SET
  ready=excluded.ready, detail=excluded.detail, checked_at=excluded.checked_at`,
		repoKey, target, boolInt(ready), detail, time.Now().Unix())
	return err
}
