package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"komi-go/internal/catalog"
	"komi-go/internal/download"
	"komi-go/internal/github"
	"komi-go/internal/installer"
	"komi-go/internal/ledger"
	"komi-go/internal/matcher"
	"komi-go/internal/platform"
)

// DownloadRequest is what the frontend sends when the user clicks download.
type DownloadRequest struct {
	RepoKey     string `json:"repoKey"`
	Tag         string `json:"tag"`
	DisplayName string `json:"displayName"`
	AssetID     int64  `json:"assetId"`
	AssetName   string `json:"assetName"`
	DownloadURL string `json:"downloadUrl"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
	PublishedAt int64  `json:"publishedAt"`
}

// StartDownload validates the request and enqueues it.
func (a *App) StartDownload(req DownloadRequest) (download.Job, error) {
	if strings.TrimSpace(req.RepoKey) == "" || strings.TrimSpace(req.AssetName) == "" || strings.TrimSpace(req.DownloadURL) == "" {
		return download.Job{}, errors.New("下载参数不完整")
	}
	if err := platform.ValidDownloadURL(req.DownloadURL); err != nil {
		return download.Job{}, err
	}
	target := catalog.CurrentTarget()
	asset := catalog.Asset{
		ID: req.AssetID, Name: req.AssetName, Size: req.Size,
		Digest: req.Digest, DownloadURL: req.DownloadURL,
	}
	// Downloads are not Windows-only: macOS/Linux packages are useful for
	// transferring to another machine. Only unknown/junk files are rejected.
	if matcher.PlatformOf(asset.Name) == "" && matcher.Score(asset.Name, target) < 0 {
		return download.Job{}, fmt.Errorf("无法识别该文件的平台：%s", asset.Name)
	}
	if matcher.IsJunk(asset.Name) {
		return download.Job{}, fmt.Errorf("这是校验文件或源码包，不能安装：%s", asset.Name)
	}

	host, owner, repo := github.SplitRepoKey(req.RepoKey)
	appID := a.appIDFor(host, owner, repo, asset.Name)
	dreq := download.Request{
		RepoKey: req.RepoKey, Asset: asset,
		DisplayName: req.DisplayName, Tag: req.Tag, PublishedAt: req.PublishedAt,
	}
	job, err := a.dl.Start(dreq)
	if err != nil {
		return download.Job{}, err
	}
	// Remember which app id this job belongs to for multi-app monorepos.
	a.mu.Lock()
	if a.jobApps == nil {
		a.jobApps = map[string]string{}
	}
	a.jobApps[job.ID] = appID
	a.mu.Unlock()
	return job, nil
}

// appIDFor keeps one ledger row per app family. A monorepo that ships several
// products gets owner/repo#slug entries after the first one.
func (a *App) appIDFor(host, owner, repo, assetName string) string {
	base := github.RepoKeyForHost(host, owner, repo)
	slug := slugOf(assetName)
	apps, _ := a.store.ListInstalled(context.Background())
	sameRepo := false
	for _, app := range apps {
		if !strings.EqualFold(app.Owner, owner) || !strings.EqualFold(app.Repo, repo) {
			continue
		}
		if app.SourceHost != "" && host != "" && !strings.EqualFold(app.SourceHost, host) {
			continue
		}
		sameRepo = true
		if strings.EqualFold(app.AppSlug, slug) ||
			(app.InstalledAssetName != "" && matcher.BaseStem(app.InstalledAssetName) == matcher.BaseStem(assetName)) {
			return app.ID
		}
	}
	if sameRepo && slug != "" {
		return base + "#" + slug
	}
	return base
}

func slugOf(assetName string) string {
	stem := strings.ToLower(matcher.BaseStem(assetName))
	stem = strings.NewReplacer("_", "-", " ", "-", ".", "-").Replace(stem)
	parts := strings.Split(stem, "-")
	if len(parts) == 0 {
		return ""
	}
	if len(parts[0]) < 3 {
		return ""
	}
	return parts[0]
}

// ListDownloads returns every queued/running/finished job of this session.
func (a *App) ListDownloads() ([]download.Job, error) {
	return a.dl.List(), nil
}

// PauseDownload stops a job, keeping the .part for resume.
func (a *App) PauseDownload(id string) error {
	return a.dl.Pause(id)
}

// ResumeDownload restarts a paused/failed job from its stored request.
func (a *App) ResumeDownload(id string) (download.Job, error) {
	return a.dl.Resume(id)
}

// CancelDownload aborts the transfer and removes the partial file.
func (a *App) CancelDownload(id string) error {
	return a.dl.Cancel(id)
}

// RetryDownload resumes a paused/failed job; canceled jobs must be re-queued
// from the repository page because their request was discarded.
func (a *App) RetryDownload(id string) (download.Job, error) {
	for _, j := range a.dl.List() {
		if j.ID != id {
			continue
		}
		switch j.Status {
		case ledger.StatusPaused, ledger.StatusFailed:
			return a.dl.Resume(id)
		case ledger.StatusDone:
			return j, nil
		default:
			return download.Job{}, errors.New("任务正在进行，无法重试")
		}
	}
	return download.Job{}, errors.New("任务不存在，请回到仓库页重新下载")
}

// ForgetDownload removes a finished row without deleting the file.
func (a *App) ForgetDownload(id string) error {
	return a.dl.Forget(id)
}

// ClearFinishedDownloads removes done/failed rows (files are kept).
func (a *App) ClearFinishedDownloads() (int, error) {
	jobs := a.dl.List()
	n := 0
	for _, j := range jobs {
		switch j.Status {
		case ledger.StatusDone, ledger.StatusFailed, ledger.StatusUnverified:
			if err := a.dl.Forget(j.ID); err == nil {
				n++
			}
		}
	}
	return n, nil
}

// OpenDownloads opens the download folder in Explorer.
func (a *App) OpenDownloads() error {
	return platform.OpenFolder(a.dl.Dir())
}

// RevealDownload selects the file in Explorer.
func (a *App) RevealDownload(id string) error {
	for _, j := range a.dl.List() {
		if j.ID != id {
			continue
		}
		if _, err := os.Stat(j.DestPath); err != nil {
			return errors.New("文件不存在，可能已被清理")
		}
		return exec.Command("explorer.exe", "/select,", j.DestPath).Start()
	}
	return errors.New("任务不存在")
}

// DiskSpace reports free/used space around the download folder.
func (a *App) DiskSpace() map[string]any {
	free, err := download.DiskFree(a.dl.Dir())
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{
		"free":  int64(free),
		"total": int64(0),
		"dir":   a.dl.Dir(),
	}
}

// InstallDownloaded extracts an archive or hands the artifact to Windows
// Installer, then confirms the result through the Uninstall registry.
func (a *App) InstallDownloaded(taskID string) (installer.Result, error) {
	job, ok := a.jobByID(taskID)
	if !ok {
		return installer.Result{}, errors.New("下载任务不存在")
	}
	if job.Status != ledger.StatusDone {
		return installer.Result{}, errors.New("文件尚未下载完成")
	}
	path := job.DestPath
	if _, err := os.Stat(path); err != nil {
		return installer.Result{}, errors.New("安装包已丢失，请重新下载")
	}

	appID := a.appIDForJob(job)
	verDir := platform.SanitizeFileName(firstNonEmpty(job.Version, job.Tag))
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".zip", ".7z", ".tar", ".tgz", ".gz":
		dest := filepath.Join(a.layout.Apps, platform.SanitizeFileName(appID), verDir)
		entry, written, err := installer.ExtractArchive(path, dest, 4<<30)
		if err != nil {
			return installer.Result{}, err
		}
		if err := a.recordArchiveInstall(appID, job, dest, entry, written); err != nil {
			return installer.Result{}, err
		}
		return installer.Result{
			Message: "已解压到 " + dest, InstallDir: dest, Size: written,
			Entry:      entry,
			Unverified: !job.Verified,
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	result, err := installer.Install(ctx, path)
	if err != nil {
		return installer.Result{}, err
	}
	if result.Pending {
		a.setPending(appID, job)
		a.emit("install:pending", map[string]any{"appId": appID, "name": job.DisplayName})
		a.settleInstallAsync(appID, job, 10*time.Minute)
		result.Unverified = !job.Verified
		return result, nil
	}

	entry, ok := a.waitConfirm(ctx, job, 15*time.Second)
	if ok {
		if err := a.recordRegistryInstall(appID, job, entry, ""); err != nil {
			return installer.Result{}, err
		}
		result.ProductCode = entry.ProductCode
		result.InstallDir = entry.InstallLocation
		result.Unverified = !job.Verified
		if result.Unverified {
			result.Message = strings.TrimSpace(result.Message + "（下载时没有可校验的摘要）")
		}
		return result, nil
	}
	a.setPending(appID, job)
	a.settleInstallAsync(appID, job, 10*time.Minute)
	result.Pending = true
	result.Unverified = !job.Verified
	if result.Message == "" {
		result.Message = "安装器已结束，正在等待系统登记"
	}
	return result, nil
}

func (a *App) jobByID(id string) (download.Job, bool) {
	for _, j := range a.dl.List() {
		if j.ID == id {
			return j, true
		}
	}
	return download.Job{}, false
}

func (a *App) appIDForJob(job download.Job) string {
	a.mu.Lock()
	id := a.jobApps[job.ID]
	a.mu.Unlock()
	if id != "" {
		return id
	}
	host, owner, repo := github.SplitRepoKey(job.RepoKey)
	return a.appIDFor(host, owner, repo, job.AssetName)
}

func (a *App) setPending(appID string, job download.Job) {
	_ = a.store.SetPending(context.Background(), appID, job.DestPath, job.Version,
		job.AssetName, job.Tag, job.Digest, job.AssetID)
}

// settleInstallAsync waits for the installer to register the product. If the
// app is still absent after the timeout the pending row stays visible with a
// "等待系统确认" badge instead of pretending the install succeeded.
func (a *App) settleInstallAsync(appID string, job download.Job, timeout time.Duration) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		entry, ok := a.waitConfirm(ctx, job, timeout)
		if !ok {
			a.emit("install:timeout", map[string]any{"appId": appID, "name": job.DisplayName})
			return
		}
		if err := a.recordRegistryInstall(appID, job, entry, ""); err != nil {
			a.emit("install:error", map[string]any{"appId": appID, "error": err.Error()})
			return
		}
		a.emit("install:done", map[string]any{"appId": appID, "name": job.DisplayName})
	}()
}

// waitConfirm tries the repo name, the asset stem and the installer filename.
func (a *App) waitConfirm(ctx context.Context, job download.Job, timeout time.Duration) (installer.UninstallEntry, bool) {
	names := []string{job.DisplayName}
	if stem := matcher.BaseStem(job.AssetName); stem != "" {
		names = append(names, stem)
	}
	if base := strings.TrimSuffix(filepath.Base(job.DestPath), filepath.Ext(job.DestPath)); base != "" {
		names = append(names, base)
	}
	return installer.ConfirmAny(ctx, names, "", timeout)
}

func (a *App) recordArchiveInstall(appID string, job download.Job, dir, entry string, size int64) error {
	ctx := context.Background()
	old, _, _ := a.store.GetInstalled(ctx, appID)
	host, owner, repo := github.SplitRepoKey(job.RepoKey)
	app := a.baseAppRecord(appID, old, host, owner, repo, job)
	app.InstallPath = entry
	app.InstallDir = dir
	app.InstallSize = size
	app.ProductCode = ""
	app.UninstallStr = ""
	app.LibraryStatus = "installed"
	app.UninstalledAt = 0
	if err := a.store.UpsertInstall(ctx, app); err != nil {
		return err
	}
	_ = a.store.ClearPending(ctx, appID)
	a.applySelectionPrefs(appID, job)
	a.emit("ledger:changed", nil)
	return nil
}

func (a *App) recordRegistryInstall(appID string, job download.Job, entry installer.UninstallEntry, knownDir string) error {
	ctx := context.Background()
	old, _, _ := a.store.GetInstalled(ctx, appID)
	host, owner, repo := github.SplitRepoKey(job.RepoKey)
	app := a.baseAppRecord(appID, old, host, owner, repo, job)

	dir := firstNonEmpty(entry.InstallLocation, knownDir)
	icon := strings.Trim(strings.TrimSpace(strings.Split(entry.DisplayIcon, ",")[0]), `"`)
	if dir == "" && icon != "" {
		dir = filepath.Dir(icon)
	}
	size := entry.EstimatedKB * 1024
	if size <= 0 && dir != "" {
		size = installer.DirSize(dir, 4<<30)
	}
	app.InstallDir = dir
	app.InstallSize = size
	app.ProductCode = entry.ProductCode
	app.UninstallStr = entry.Uninstall
	app.InstallPath = icon
	if app.InstallPath == "" {
		app.InstallPath = old.InstallPath
	}
	app.LibraryStatus = "installed"
	app.UninstalledAt = 0
	if err := a.store.UpsertInstall(ctx, app); err != nil {
		return err
	}
	_ = a.store.ClearPending(ctx, appID)
	a.applySelectionPrefs(appID, job)
	a.emit("ledger:changed", nil)
	return nil
}

// baseAppRecord preserves fields owned by the update checker and the user.
func (a *App) baseAppRecord(appID string, old ledger.InstalledApp, host, owner, repo string, job download.Job) ledger.InstalledApp {
	slug := slugOf(job.AssetName)
	if old.AppSlug != "" {
		slug = old.AppSlug
	}
	name := job.DisplayName
	if name == "" {
		name = displayName(owner, repo)
	}
	app := ledger.InstalledApp{
		ID: appID, Owner: owner, Repo: repo, DisplayName: name,
		InstalledVersion:      firstNonEmpty(job.Version, job.Tag),
		InstalledAssetName:    job.AssetName,
		InstalledAssetID:      job.AssetID,
		InstalledAssetDigest:  job.Digest,
		InstalledAssetURL:     job.AssetURL,
		InstalledReleaseTag:   job.Tag,
		InstalledReleasePubAt: job.PublishedAt,
		InstallSource:         firstNonEmpty(host, "github.com"),
		SourceHost:            firstNonEmpty(host, "github.com"),
		AppSlug:               slug,
		LibraryStatus:         "installed",
		UpdateCheckEnabled:    true,
		InstalledAt:           time.Now().Unix(),
	}
	if old.ID != "" {
		app.InstalledAt = old.InstalledAt
		app.Pinned = old.Pinned
		app.SkippedReleaseTag = old.SkippedReleaseTag
		app.SkippedPubAt = old.SkippedPubAt
		app.UpdateCheckEnabled = old.UpdateCheckEnabled
		app.AssetGlob = old.AssetGlob
		app.AssetFilter = old.AssetFilter
		app.IncludePrerelease = old.IncludePrerelease
		app.FallbackOlder = old.FallbackOlder
		app.PreferredVariant = old.PreferredVariant
	}
	return app
}

// applySelectionPrefs learns the asset family so the next update stays on the
// same variant (arm64 does not get replaced by universal).
func (a *App) applySelectionPrefs(appID string, job download.Job) {
	ctx := context.Background()
	glob := matcher.TagGlob(job.AssetName, job.Tag)
	variant := matcher.ArchOf(job.AssetName)
	includePre, _ := a.boolSetting("update.prerelease", false)
	fallback, ok := a.boolSetting("update.fallback_older", true)
	if !ok {
		fallback = true
	}
	filter, _ := a.store.GetSetting(ctx, "update.filter")
	view, ok2 := a.cachedView(strings.Split(job.RepoKey, "#")[0])
	picked, siblings := 0, 0
	if ok2 {
		for _, rel := range view.Releases {
			if rel.TagName != job.Tag {
				continue
			}
			for i, asset := range rel.Assets {
				if asset.ID == job.AssetID {
					picked = i
				}
				if matcher.Score(asset.Name, view.Target) >= 0 &&
					matcher.BaseStem(asset.Name) == matcher.BaseStem(job.AssetName) {
					siblings++
				}
			}
		}
	}
	_ = a.store.SetSelectionPrefs(ctx, appID, glob, variant, filter, includePre, fallback, picked, siblings)
}

// ---------------------------------------------------------------------------
// update helpers
// ---------------------------------------------------------------------------

// UpdateApp re-downloads the latest known asset for an installed app.
func (a *App) UpdateApp(id string) (download.Job, error) {
	ctx := context.Background()
	app, ok, err := a.store.GetInstalled(ctx, id)
	if err != nil {
		return download.Job{}, err
	}
	if !ok {
		return download.Job{}, errors.New("应用不存在")
	}
	if app.LatestAssetURL == "" {
		return download.Job{}, errors.New("还没有可用的新版本信息，请先检查更新")
	}
	host := app.SourceHost
	if host == "" {
		host = "github.com"
	}
	req := DownloadRequest{
		RepoKey:     github.RepoKeyForHost(host, app.Owner, app.Repo),
		Tag:         app.LatestReleaseTag,
		DisplayName: app.DisplayName,
		AssetID:     app.LatestAssetID,
		AssetName:   app.LatestAssetName,
		DownloadURL: app.LatestAssetURL,
		Size:        app.LatestAssetSize,
		Digest:      app.LatestAssetDigest,
		PublishedAt: app.LatestReleasePubAt,
	}
	return a.StartDownload(req)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
