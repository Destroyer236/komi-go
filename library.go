package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"komi-go/internal/catalog"
	"komi-go/internal/download"
	"komi-go/internal/github"
	"komi-go/internal/installer"
	"komi-go/internal/ledger"
	"komi-go/internal/matcher"
	"komi-go/internal/platform"
	"komi-go/internal/winreg"
)

// ListInstalled returns the local ledger.
func (a *App) ListInstalled() ([]ledger.InstalledApp, error) {
	apps, err := a.store.ListInstalled(context.Background())
	if err != nil {
		return nil, err
	}
	return apps, nil
}

// LibraryStats is the summary strip above the library list.
func (a *App) LibraryStats() map[string]any {
	apps, _ := a.store.ListInstalled(context.Background())
	stats := map[string]any{
		"total": 0, "installed": 0, "updates": 0, "tracked": 0,
		"pending": 0, "uninstalled": 0, "size": int64(0),
	}
	installed, updates, tracked, pending, uninstalled := 0, 0, 0, 0, 0
	var size int64
	for _, app := range apps {
		stats["total"] = stats["total"].(int) + 1
		switch app.LibraryStatus {
		case "tracked":
			tracked++
		case "uninstalled":
			uninstalled++
		default:
			installed++
		}
		if app.UpdateAvailable {
			updates++
		}
		if app.PendingVersion != "" || app.PendingInstallPath != "" {
			pending++
		}
		size += app.InstallSize
	}
	stats["installed"] = installed
	stats["updates"] = updates
	stats["tracked"] = tracked
	stats["pending"] = pending
	stats["uninstalled"] = uninstalled
	stats["size"] = size
	if free, err := download.DiskFree(a.dl.Dir()); err == nil {
		stats["diskFree"] = int64(free)
	}
	var lastCheck int64
	a.mu.RLock()
	if !a.autoLastRun.IsZero() {
		lastCheck = a.autoLastRun.Unix()
	}
	a.mu.RUnlock()
	stats["lastCheck"] = lastCheck
	return stats
}

// ---------------------------------------------------------------------------
// open / uninstall / bookkeeping
// ---------------------------------------------------------------------------

// OpenApp launches the installed executable, falling back to its folder.
func (a *App) OpenApp(id string) error {
	app, ok, err := a.store.GetInstalled(context.Background(), id)
	if err != nil {
		return err
	}
	if !ok || app.LibraryStatus == "uninstalled" {
		return errors.New("应用未安装")
	}
	path := strings.Trim(strings.TrimSpace(app.InstallPath), `"`)
	if path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			cmd := exec.Command(path)
			if app.InstallDir != "" {
				cmd.Dir = app.InstallDir
			}
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
	}
	if app.InstallDir != "" {
		return platform.OpenFolder(app.InstallDir)
	}
	return errors.New("找不到程序位置，请重新安装或在系统中打开")
}

// OpenInstallDir reveals the real install directory found in the registry.
func (a *App) OpenInstallDir(id string) error {
	app, ok, err := a.store.GetInstalled(context.Background(), id)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("应用不存在")
	}
	if app.InstallDir == "" {
		return errors.New("还没有记录到安装目录，可先点击“同步安装信息”")
	}
	return platform.OpenFolder(app.InstallDir)
}

// UninstallApp runs the vendor uninstaller. Registry is the source of truth;
// portable archives under %LocalAppData%\KomiGo\apps are removed directly.
func (a *App) UninstallApp(id string) (installer.Result, error) {
	ctx := context.Background()
	app, ok, err := a.store.GetInstalled(ctx, id)
	if err != nil {
		return installer.Result{}, err
	}
	if !ok {
		return installer.Result{}, errors.New("应用不存在")
	}
	if app.LibraryStatus == "uninstalled" {
		return installer.Result{Message: "记录里已经是未安装状态"}, nil
	}

	if app.ProductCode != "" || app.UninstallStr != "" {
		uctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		result, err := installer.Uninstall(uctx, app.ProductCode, app.UninstallStr)
		if err != nil {
			return installer.Result{}, err
		}
		if err := a.store.MarkUninstalled(ctx, id); err != nil {
			return installer.Result{}, err
		}
		a.emit("ledger:changed", nil)
		return result, nil
	}

	// Portable archive: only delete a directory we created ourselves.
	dir := app.InstallDir
	if dir == "" || !isUnder(dir, a.layout.Apps) {
		return installer.Result{}, errors.New("没有系统卸载入口，且安装目录不在 Komi 管理范围内，请手动删除")
	}
	if err := os.RemoveAll(dir); err != nil {
		return installer.Result{}, fmt.Errorf("删除安装目录失败：%w", err)
	}
	if err := a.store.MarkUninstalled(ctx, id); err != nil {
		return installer.Result{}, err
	}
	a.emit("ledger:changed", nil)
	return installer.Result{Message: "已删除 " + dir}, nil
}

func isUnder(path, root string) bool {
	absPath, err1 := filepath.Abs(path)
	absRoot, err2 := filepath.Abs(root)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// UpdateInstallInfo re-reads the registry so a moved/updated app gets its real
// directory and size without reinstalling.
func (a *App) UpdateInstallInfo(id string) (ledger.InstalledApp, error) {
	ctx := context.Background()
	app, ok, err := a.store.GetInstalled(ctx, id)
	if err != nil {
		return ledger.InstalledApp{}, err
	}
	if !ok {
		return ledger.InstalledApp{}, errors.New("应用不存在")
	}
	var entry installer.UninstallEntry
	found := false
	if app.ProductCode != "" {
		entry, found = winreg.FindByProductCode(app.ProductCode)
	}
	if !found && app.InstallDir != "" {
		entry, found = winreg.FindByLocation(app.InstallDir)
	}
	if !found && app.DisplayName != "" {
		entry, found = winreg.FindByName(app.DisplayName)
	}
	if !found {
		if app.InstallDir != "" && isUnder(app.InstallDir, a.layout.Apps) {
			if info, err := os.Stat(app.InstallDir); err == nil && info.IsDir() {
				size := installer.DirSize(app.InstallDir, 4<<30)
				_ = a.store.UpdateInstallMeta(ctx, id, app.InstallDir, "", "", size, app.InstalledVersionCode)
				app.InstallSize = size
				a.emit("ledger:changed", nil)
				return app, nil
			}
		}
		if app.LibraryStatus == "installed" {
			_ = a.store.MarkUninstalled(ctx, id)
			a.emit("ledger:changed", nil)
			return ledger.InstalledApp{}, errors.New("系统里已找不到这个应用，已标记为未安装")
		}
		return app, nil
	}
	dir := entry.InstallLocation
	if dir == "" {
		dir = filepath.Dir(entry.DisplayIcon)
	}
	size := entry.EstimatedKB * 1024
	if size <= 0 && dir != "" {
		size = installer.DirSize(dir, 4<<30)
	}
	if err := a.store.UpdateInstallMeta(ctx, id, dir, entry.ProductCode, entry.Uninstall, size, app.InstalledVersionCode); err != nil {
		return ledger.InstalledApp{}, err
	}
	a.emit("ledger:changed", nil)
	app, _, _ = a.store.GetInstalled(ctx, id)
	return app, nil
}

// RemoveInstalled forgets a ledger row. An installed app must be uninstalled
// first so the library cannot lie about the machine state.
func (a *App) RemoveInstalled(id string) error {
	ctx := context.Background()
	app, ok, err := a.store.GetInstalled(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if app.LibraryStatus == "installed" && a.stillInstalled(app) {
		return errors.New("这个应用仍安装在系统里，请先卸载，再移除记录")
	}
	if err := a.store.DeleteInstalled(ctx, id); err != nil {
		return err
	}
	a.emit("ledger:changed", nil)
	return nil
}

func (a *App) stillInstalled(app ledger.InstalledApp) bool {
	if app.ProductCode != "" {
		if _, ok := winreg.FindByProductCode(app.ProductCode); ok {
			return true
		}
	}
	if app.InstallDir != "" {
		if _, ok := winreg.FindByLocation(app.InstallDir); ok {
			return true
		}
	}
	if app.InstallPath != "" {
		if info, err := os.Stat(app.InstallPath); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// RestoreTracking clears the uninstalled flag.
func (a *App) RestoreTracking(id string) error {
	if err := a.store.RestoreTracking(context.Background(), id); err != nil {
		return err
	}
	a.emit("ledger:changed", nil)
	return nil
}

// SetPinned suppresses update prompts for an app.
func (a *App) SetPinned(id string, pinned bool) error {
	if err := a.store.SetPinned(context.Background(), id, pinned); err != nil {
		return err
	}
	a.emit("ledger:changed", nil)
	return nil
}

// SetUpdateCheck toggles automatic checks for one app.
func (a *App) SetUpdateCheck(id string, enabled bool) error {
	if err := a.store.SetUpdateCheck(context.Background(), id, enabled); err != nil {
		return err
	}
	a.emit("ledger:changed", nil)
	return nil
}

// SkipVersion hides the current latest tag.
func (a *App) SkipVersion(id string) error {
	app, ok, err := a.store.GetInstalled(context.Background(), id)
	if err != nil || !ok {
		return errors.New("应用不存在")
	}
	if app.LatestReleaseTag == "" {
		return errors.New("还没有可跳过的版本")
	}
	if err := a.store.SkipVersion(context.Background(), id, app.LatestReleaseTag, app.LatestReleasePubAt); err != nil {
		return err
	}
	a.emit("ledger:changed", nil)
	return nil
}

// SetAppPreferences stores the per-app asset filter and prerelease policy.
func (a *App) SetAppPreferences(id, filter string, includePre, fallbackOlder bool) error {
	if err := matcher.AssetFilterError(filter); err != nil {
		return err
	}
	app, ok, err := a.store.GetInstalled(context.Background(), id)
	if err != nil || !ok {
		return errors.New("应用不存在")
	}
	return a.store.SetSelectionPrefs(context.Background(), id, app.AssetGlob, app.PreferredVariant,
		strings.TrimSpace(filter), includePre, fallbackOlder, app.PickedIndex, app.SiblingCount)
}

// ---------------------------------------------------------------------------
// updates
// ---------------------------------------------------------------------------

// CheckUpdates probes every tracked repository once and writes the verdict.
func (a *App) CheckUpdates(force bool) ([]ledger.InstalledApp, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	apps, err := a.store.ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	target := catalog.CurrentTarget()

	type group struct {
		host  string
		owner string
		repo  string
		apps  []ledger.InstalledApp
	}
	groups := map[string]*group{}
	for _, app := range apps {
		if !app.UpdateCheckEnabled || app.LibraryStatus != "installed" {
			continue
		}
		host := app.SourceHost
		if host == "" {
			host = "github.com"
		}
		key := strings.ToLower(host + "/" + app.Owner + "/" + app.Repo)
		g, ok := groups[key]
		if !ok {
			g = &group{host: host, owner: app.Owner, repo: app.Repo}
			groups[key] = g
		}
		g.apps = append(g.apps, app)
	}

	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, g := range groups {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			releases, _, err := a.clientFor(g.host).Releases(ctx, g.owner, g.repo, 40, force)
			if err != nil {
				for _, app := range g.apps {
					_ = a.store.SetCheckError(ctx, app.ID, updErrText(err))
				}
				return
			}
			releases = matcher.SortReleases(releases)
			for _, app := range g.apps {
				identity := matcher.AppIdentity{
					AssetName:  app.InstalledAssetName,
					AssetGlob:  app.AssetGlob,
					Slug:       app.AppSlug,
					VersionTag: app.InstalledReleaseTag,
				}
				sel := matcher.SelectRelease(releases, target, identity,
					app.IncludePrerelease, app.FallbackOlder, app.AssetFilter)
				if sel.Asset == nil {
					_ = a.store.SetCheckError(ctx, app.ID, "没有找到适用于本机的资产")
					continue
				}
				in := matcher.UpdateInput{
					InstalledTag:         app.InstalledReleaseTag,
					InstalledAssetID:     app.InstalledAssetID,
					InstalledDigest:      app.InstalledAssetDigest,
					InstalledSize:        int64(0),
					InstalledPublishedAt: unixTime(app.InstalledReleasePubAt),
					CandidateTag:         sel.Release.TagName,
					CandidateAssetID:     sel.Asset.ID,
					CandidateDigest:      sel.Asset.Digest,
					CandidateSize:        sel.Asset.Size,
					CandidatePublishedAt: sel.Release.PublishedAt,
					SkippedTag:           app.SkippedReleaseTag,
					SkippedPublishedAt:   unixTime(app.SkippedPubAt),
					Pinned:               app.Pinned,
					IncludePrerelease:    app.IncludePrerelease,
				}
				update, _ := matcher.DecideUpdate(in)
				_ = a.store.UpdateCheckResult(ctx, app.ID, sel.Release, sel.Asset, update, "")
				_ = a.store.SetSelectionPrefs(ctx, app.ID, app.AssetGlob, app.PreferredVariant,
					app.AssetFilter, app.IncludePrerelease, app.FallbackOlder, sel.PickedIndex, sel.SiblingCount)
			}
		}()
	}
	wg.Wait()
	out, err := a.store.ListInstalled(ctx)
	if err == nil {
		a.mu.Lock()
		a.autoLastRun = time.Now()
		a.mu.Unlock()
		a.emit("ledger:changed", nil)
	}
	return out, err
}

// ---------------------------------------------------------------------------
// import / export
// ---------------------------------------------------------------------------

// ExportApp is the custom backup row. Field names follow the handbook.
type ExportApp struct {
	PackageName           string `json:"packageName"`
	RepoOwner             string `json:"repoOwner"`
	RepoName              string `json:"repoName"`
	SourceHost            string `json:"sourceHost"`
	AppName               string `json:"appName"`
	PreferredAssetVariant string `json:"preferredAssetVariant"`
	AssetFilterRegex      string `json:"assetFilterRegex"`
	IncludePreReleases    bool   `json:"includePreReleases"`
	FallbackToOlder       bool   `json:"fallbackToOlderReleases"`
	SkippedReleaseTag     string `json:"skippedReleaseTag"`
	UpdateCheckEnabled    bool   `json:"updateCheckEnabled"`

	InstalledVersion string `json:"installedVersion,omitempty"`
	InstallDir       string `json:"installDir,omitempty"`
	InstallPath      string `json:"installPath,omitempty"`
	LibraryStatus    string `json:"libraryStatus,omitempty"`
}

// LibraryBundle is the JSON file shape.
type LibraryBundle struct {
	SchemaVersion int         `json:"schemaVersion"`
	ExportedAt    time.Time   `json:"exportedAt"`
	Apps          []ExportApp `json:"apps"`
}

// ImportPreview is one row of the dry-run.
type ImportPreview struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status"` // new | exists | invalid
	Reason string `json:"reason,omitempty"`
}

// ImportReport is returned by the dry-run and the apply step.
type ImportReport struct {
	Total   int             `json:"total"`
	Added   int             `json:"added"`
	Skipped int             `json:"skipped"`
	Invalid int             `json:"invalid"`
	Rows    []ImportPreview `json:"rows"`
}

type importBundle struct {
	payload string
	apps    []ExportApp
	invalid []ImportPreview
}

// ExportLibraryFile opens a save dialog and writes the custom backup.
func (a *App) ExportLibraryFile() (string, error) {
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "导出 Komi 库",
		DefaultFilename: "komi-library-" + time.Now().Format("2006-01-02") + ".json",
		Filters:         []wruntime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}},
	})
	if err != nil || path == "" {
		if err != nil {
			return "", err
		}
		return "", errors.New("已取消")
	}
	apps, err := a.store.ListInstalled(context.Background())
	if err != nil {
		return "", err
	}
	bundle := LibraryBundle{SchemaVersion: 1, ExportedAt: time.Now().UTC(), Apps: make([]ExportApp, 0, len(apps))}
	for _, app := range apps {
		host := app.SourceHost
		if host == "" {
			host = "github.com"
		}
		bundle.Apps = append(bundle.Apps, ExportApp{
			PackageName: app.ID, RepoOwner: app.Owner, RepoName: app.Repo,
			SourceHost: host, AppName: app.DisplayName,
			PreferredAssetVariant: app.PreferredVariant,
			AssetFilterRegex:      app.AssetFilter,
			IncludePreReleases:    app.IncludePrerelease,
			FallbackToOlder:       app.FallbackOlder,
			SkippedReleaseTag:     app.SkippedReleaseTag,
			UpdateCheckEnabled:    app.UpdateCheckEnabled,
			InstalledVersion:      app.InstalledVersion,
			InstallDir:            app.InstallDir,
			InstallPath:           app.InstallPath,
			LibraryStatus:         app.LibraryStatus,
		})
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ExportObtainiumFile emits the Obtainium-compatible array.
func (a *App) ExportObtainiumFile() (string, error) {
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "导出为 Obtainium 格式",
		DefaultFilename: "komi-obtainium-" + time.Now().Format("2006-01-02") + ".json",
		Filters:         []wruntime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}},
	})
	if err != nil || path == "" {
		if err != nil {
			return "", err
		}
		return "", errors.New("已取消")
	}
	apps, err := a.store.ListInstalled(context.Background())
	if err != nil {
		return "", err
	}
	type obtainium struct {
		ID                 string         `json:"id"`
		URL                string         `json:"url"`
		Author             string         `json:"author"`
		Name               string         `json:"name"`
		AdditionalSettings map[string]any `json:"additionalSettings,omitempty"`
	}
	out := make([]obtainium, 0, len(apps))
	for _, app := range apps {
		host := app.SourceHost
		if host == "" {
			host = "github.com"
		}
		item := obtainium{
			ID: app.ID, URL: "https://" + host + "/" + app.Owner + "/" + app.Repo,
			Author: app.Owner, Name: app.DisplayName,
		}
		if app.AssetFilter != "" || app.FallbackOlder || app.IncludePrerelease {
			item.AdditionalSettings = map[string]any{
				"apkFilterRegEx":          app.AssetFilter,
				"fallbackToOlderReleases": app.FallbackOlder,
				"includePrereleases":      app.IncludePrerelease,
			}
		}
		out = append(out, item)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ImportLibraryFile opens a file picker, parses the bundle and runs a dry-run
// in memory. Applying is a separate explicit call.
func (a *App) ImportLibraryFile() (ImportReport, error) {
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:   "导入 Komi 库",
		Filters: []wruntime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}},
	})
	if err != nil {
		return ImportReport{}, err
	}
	if path == "" {
		return ImportReport{}, errors.New("已取消")
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 8<<20 {
		return ImportReport{}, errors.New("文件过大（上限 8 MB）")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ImportReport{}, err
	}
	if len(data) > 8<<20 {
		return ImportReport{}, errors.New("文件过大（上限 8 MB）")
	}
	apps, invalid, err := parseLibrary(data)
	if err != nil {
		return ImportReport{}, err
	}
	a.mu.Lock()
	a.lastImport = &importBundle{apps: apps, invalid: invalid}
	a.mu.Unlock()
	return a.previewImport(apps, invalid), nil
}

// ApplyImport writes the last dry-run.
func (a *App) ApplyImport() (ImportReport, error) {
	a.mu.Lock()
	bundle := a.lastImport
	a.mu.Unlock()
	if bundle == nil {
		return ImportReport{}, errors.New("请先选择要导入的文件")
	}
	ctx := context.Background()
	report := ImportReport{
		Total:   len(bundle.apps) + len(bundle.invalid),
		Invalid: len(bundle.invalid),
	}
	report.Rows = append(report.Rows, bundle.invalid...)
	for _, item := range bundle.apps {
		key := github.RepoKeyForHost(item.SourceHost, item.RepoOwner, item.RepoName)
		if existing, ok, _ := a.store.GetInstalled(ctx, key); ok {
			report.Skipped++
			report.Rows = append(report.Rows, ImportPreview{Key: key, Name: item.AppName, Status: "exists", Reason: "已存在：" + existing.DisplayName})
			continue
		}
		app := ledger.InstalledApp{
			ID: key, Owner: item.RepoOwner, Repo: item.RepoName,
			DisplayName:        item.AppName,
			SourceHost:         firstNonEmpty(item.SourceHost, "github.com"),
			AppSlug:            "",
			LibraryStatus:      "tracked",
			UpdateCheckEnabled: item.UpdateCheckEnabled,
			AssetFilter:        item.AssetFilterRegex,
			IncludePrerelease:  item.IncludePreReleases,
			FallbackOlder:      item.FallbackToOlder,
			PreferredVariant:   item.PreferredAssetVariant,
			SkippedReleaseTag:  item.SkippedReleaseTag,
			InstalledAt:        time.Now().Unix(),
		}
		if err := a.store.UpsertInstall(ctx, app); err != nil {
			report.Invalid++
			report.Rows = append(report.Rows, ImportPreview{Key: key, Name: item.AppName, Status: "invalid", Reason: err.Error()})
			continue
		}
		report.Added++
		report.Rows = append(report.Rows, ImportPreview{Key: key, Name: item.AppName, Status: "new"})
	}
	a.emit("ledger:changed", nil)
	return report, nil
}

func (a *App) previewImport(apps []ExportApp, invalid []ImportPreview) ImportReport {
	report := ImportReport{Total: len(apps) + len(invalid), Invalid: len(invalid)}
	report.Rows = append(report.Rows, invalid...)
	ctx := context.Background()
	for _, item := range apps {
		key := github.RepoKeyForHost(item.SourceHost, item.RepoOwner, item.RepoName)
		if _, ok, _ := a.store.GetInstalled(ctx, key); ok {
			report.Skipped++
			report.Rows = append(report.Rows, ImportPreview{Key: key, Name: item.AppName, Status: "exists", Reason: "已在库中"})
			continue
		}
		report.Added++
		report.Rows = append(report.Rows, ImportPreview{Key: key, Name: item.AppName, Status: "new"})
	}
	return report
}

func parseLibrary(data []byte) ([]ExportApp, []ImportPreview, error) {
	var bundle LibraryBundle
	if err := json.Unmarshal(data, &bundle); err == nil {
		if len(bundle.Apps) == 0 {
			return nil, nil, errors.New("备份里没有应用")
		}
		return normalizeImport(bundle.Apps)
	}
	var ob []map[string]any
	if err := json.Unmarshal(data, &ob); err != nil {
		return nil, nil, errors.New("无法识别的 JSON：既不是 Komi 备份，也不是 Obtainium 列表")
	}
	apps := make([]ExportApp, 0, len(ob))
	invalid := make([]ImportPreview, 0)
	for _, item := range ob {
		url, _ := item["url"].(string)
		ref, err := github.ParseRef(url)
		if err != nil {
			name, _ := item["name"].(string)
			if name == "" {
				name = "未命名条目"
			}
			invalid = append(invalid, ImportPreview{Name: name, Status: "invalid", Reason: "无法解析仓库地址：" + url})
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			name = ref.Name
		}
		app := ExportApp{
			PackageName: fmt.Sprint(item["id"]),
			RepoOwner:   ref.Owner, RepoName: ref.Name, SourceHost: ref.Host,
			AppName: name, FallbackToOlder: true, UpdateCheckEnabled: true,
		}
		if settings, ok := item["additionalSettings"].(map[string]any); ok {
			if v, ok := settings["apkFilterRegEx"].(string); ok {
				app.AssetFilterRegex = v
			}
			if v, ok := settings["fallbackToOlderReleases"].(bool); ok {
				app.FallbackToOlder = v
			}
			if v, ok := settings["includePrereleases"].(bool); ok {
				app.IncludePreReleases = v
			}
		}
		apps = append(apps, app)
	}
	out, normalizedInvalid, err := normalizeImport(apps)
	if err != nil {
		return nil, nil, err
	}
	return out, append(invalid, normalizedInvalid...), nil
}

func normalizeImport(apps []ExportApp) ([]ExportApp, []ImportPreview, error) {
	if len(apps) > 500 {
		return nil, nil, errors.New("一次最多导入 500 个应用")
	}
	out := apps[:0]
	invalid := make([]ImportPreview, 0)
	seen := map[string]bool{}
	for _, app := range apps {
		name := strings.TrimSpace(app.AppName)
		if name == "" {
			name = strings.TrimSpace(app.RepoName)
		}
		if app.RepoOwner == "" || app.RepoName == "" {
			invalid = append(invalid, ImportPreview{Name: name, Status: "invalid", Reason: "缺少 owner/repo"})
			continue
		}
		if err := github.CheckOwnerRepo(app.RepoOwner, app.RepoName); err != nil {
			invalid = append(invalid, ImportPreview{Name: name, Status: "invalid", Reason: err.Error()})
			continue
		}
		if err := matcher.AssetFilterError(app.AssetFilterRegex); err != nil {
			app.AssetFilterRegex = ""
		}
		if app.SourceHost == "" {
			app.SourceHost = "github.com"
		}
		if app.AppName == "" {
			app.AppName = app.RepoName
		}
		key := github.RepoKeyForHost(app.SourceHost, app.RepoOwner, app.RepoName)
		if seen[key] {
			invalid = append(invalid, ImportPreview{Key: key, Name: name, Status: "invalid", Reason: "文件内重复条目"})
			continue
		}
		seen[key] = true
		out = append(out, app)
	}
	if len(out) == 0 {
		return nil, invalid, errors.New("文件里没有可导入的仓库")
	}
	return out, invalid, nil
}

// ---------------------------------------------------------------------------
// external apps
// ---------------------------------------------------------------------------

// ExternalApp is one row from "Apps & features" that Komi does not track.
type ExternalApp struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Publisher    string `json:"publisher"`
	InstallDir   string `json:"installDir"`
	DisplayIcon  string `json:"displayIcon"`
	ProductCode  string `json:"productCode"`
	UninstallStr string `json:"uninstallString"`
	Size         int64  `json:"size"`
}

// ScanExternalApps enumerates the Uninstall registry, skipping entries already
// in the library.
func (a *App) ScanExternalApps() ([]ExternalApp, error) {
	entries, err := winreg.ListUninstall()
	if err != nil {
		return nil, fmt.Errorf("读取系统应用列表失败：%w", err)
	}
	apps, _ := a.store.ListInstalled(context.Background())
	tracked := map[string]bool{}
	for _, app := range apps {
		if app.DisplayName != "" {
			tracked[strings.ToLower(app.DisplayName)] = true
		}
		if app.InstallDir != "" {
			tracked[strings.ToLower(filepath.Clean(app.InstallDir))] = true
		}
	}
	out := make([]ExternalApp, 0, 80)
	for _, e := range entries {
		if tracked[strings.ToLower(e.Name)] {
			continue
		}
		if e.InstallLocation != "" && tracked[strings.ToLower(filepath.Clean(e.InstallLocation))] {
			continue
		}
		if strings.TrimSpace(e.Name) == "" || strings.HasPrefix(e.Name, "Microsoft Visual C++") {
			continue
		}
		out = append(out, ExternalApp{
			Name: e.Name, Version: e.Version, Publisher: e.Publisher,
			InstallDir: e.InstallLocation, DisplayIcon: e.DisplayIcon,
			ProductCode: e.ProductCode, UninstallStr: e.Uninstall,
			Size: e.EstimatedKB * 1024,
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// MatchExternalApp searches GitHub for a registry app name and verifies that
// the candidates publish installable Windows assets.
func (a *App) MatchExternalApp(name string) ([]catalog.RepoInfo, error) {
	term := sanitizeSearchTerm(name)
	if term == "" {
		return nil, errors.New("应用名为空，无法匹配")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	query := fmt.Sprintf(`"%s" in:name stars:>3 archived:false`, term)
	repos, _, err := a.clientFor("github.com").Search(ctx, query, 1, 5, "stars", "", false)
	if err != nil {
		return nil, errors.New(friendly(err))
	}
	if len(repos) > 4 {
		repos = repos[:4]
	}
	a.probeRepos(ctx, repos)
	return repos, nil
}

func sanitizeSearchTerm(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune(' ')
		}
	}
	return strings.TrimSpace(b.String())
}

// TrackApp adds a tracked-only row (import, external scan or manual follow).
func (a *App) TrackApp(host, owner, repo, name string) error {
	if err := github.CheckOwnerRepo(owner, repo); err != nil {
		return err
	}
	key := github.RepoKeyForHost(host, owner, repo)
	ctx := context.Background()
	if _, ok, _ := a.store.GetInstalled(ctx, key); ok {
		return errors.New("这个仓库已经在库里了")
	}
	if name == "" {
		name = repo
	}
	app := ledger.InstalledApp{
		ID: key, Owner: owner, Repo: repo, DisplayName: name,
		SourceHost:    firstNonEmpty(host, "github.com"),
		LibraryStatus: "tracked", UpdateCheckEnabled: true,
		InstalledAt: time.Now().Unix(),
	}
	if err := a.store.UpsertInstall(ctx, app); err != nil {
		return err
	}
	a.emit("ledger:changed", nil)
	return nil
}
