package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"komi-go/internal/github"
	"komi-go/internal/platform"
	"komi-go/internal/selfupdate"
	"komi-go/internal/winreg"
)

// ---------------------------------------------------------------------------
// window
// ---------------------------------------------------------------------------

// WindowMinimise is called by the frameless title bar.
func (a *App) WindowMinimise() { wruntime.WindowMinimise(a.ctx) }

// WindowMaximise toggles maximise.
func (a *App) WindowMaximise() {
	if wruntime.WindowIsMaximised(a.ctx) {
		wruntime.WindowUnmaximise(a.ctx)
		return
	}
	wruntime.WindowMaximise(a.ctx)
}

// WindowClose hides to tray or quits, matching the setting.
func (a *App) WindowClose() {
	closeToTray, _ := a.boolSetting("ui.close_to_tray", true)
	if closeToTray && a.isTrayReady() {
		a.hideWindow()
		return
	}
	a.Quit()
}

// HideWindow sends the app to the tray.
func (a *App) HideWindow() { a.hideWindow() }

func (a *App) hideWindow() {
	if a.ctx != nil {
		wruntime.WindowHide(a.ctx)
	}
}

func (a *App) showWindow() {
	if a.ctx != nil {
		wruntime.WindowShow(a.ctx)
		wruntime.WindowUnminimise(a.ctx)
	}
}

// Quit stops the downloader cleanly and exits.
func (a *App) Quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	a.stopTray()
	wruntime.Quit(a.ctx)
}

// beforeClose is the Wails OnBeforeClose hook: return true to veto the close.
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	quitting := a.quitting
	a.mu.Unlock()
	if quitting {
		return false
	}
	closeToTray, _ := a.boolSetting("ui.close_to_tray", true)
	if closeToTray && a.isTrayReady() {
		a.hideWindow()
		return true
	}
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	return false
}

// ---------------------------------------------------------------------------
// single instance / deep links
// ---------------------------------------------------------------------------

// onSecondInstance shows the window and forwards a deep link.
func (a *App) onSecondInstance(args []string) {
	a.showWindow()
	if ref, ok := a.deepLinkFromArgs(args); ok {
		a.emit("deeplink", ref)
	}
}

func (a *App) deepLinkFromArgs(args []string) (map[string]string, bool) {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if !strings.HasPrefix(strings.ToLower(arg), "komi://") &&
			!strings.Contains(arg, "/releases/tag/") {
			continue
		}
		ref, err := github.ParseRef(arg)
		if err != nil {
			continue
		}
		if ref.Owner == "" || ref.Name == "" {
			continue
		}
		host := ref.Host
		if host == "" {
			host = "github.com"
		}
		return map[string]string{
			"host": host, "owner": ref.Owner, "repo": ref.Name, "tag": ref.Tag,
			"key": github.RepoKeyForHost(host, ref.Owner, ref.Name),
		}, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// autostart / protocol
// ---------------------------------------------------------------------------

// SetAutostart adds or removes the HKCU Run entry.
func (a *App) SetAutostart(enabled bool) error {
	if err := winreg.SetAutostart(a.exePath(), enabled, true); err != nil {
		return fmt.Errorf("无法修改开机启动：%w", err)
	}
	_ = a.store.SetSetting(context.Background(), "system.autostart", boolText(enabled))
	a.emit("settings:changed", map[string]string{"system.autostart": boolText(enabled)})
	return nil
}

// AutostartState re-reads the registry instead of trusting the DB.
func (a *App) AutostartState() bool { return winreg.AutostartEnabled() }

// RegisterURLScheme wires komi:// to this executable.
func (a *App) RegisterURLScheme() error {
	if err := winreg.EnsureURLScheme(a.exePath()); err != nil {
		return err
	}
	a.emit("settings:changed", map[string]string{"urlScheme": "true"})
	return nil
}

// URLSchemeInfo reports the protocol status.
func (a *App) URLSchemeInfo() map[string]any {
	return map[string]any{
		"registered": winreg.URLSchemeRegistered(a.exePath()),
		"exe":        a.exePath(),
		"example":    "komi://github.com/owner/repo",
	}
}

// ---------------------------------------------------------------------------
// auto update checks
// ---------------------------------------------------------------------------

func (a *App) startScheduler() {
	a.schedulerStop = make(chan struct{})
	go func() {
		// First run: honour the saved interval but also refresh if the app was
		// closed for longer than that.
		a.maybeAutoCheck()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-a.schedulerStop:
				return
			case <-ticker.C:
				a.maybeAutoCheck()
			}
		}
	}()
}

func (a *App) stopScheduler() {
	if a.schedulerStop != nil {
		close(a.schedulerStop)
		a.schedulerStop = nil
	}
}

func (a *App) maybeAutoCheck() {
	auto, _ := a.boolSetting("update.auto", true)
	if !auto {
		return
	}
	interval := 12
	if raw, err := a.store.GetSetting(context.Background(), "update.interval"); err == nil && raw != "" {
		fmt.Sscanf(raw, "%d", &interval)
	}
	if interval < 1 {
		interval = 12
	}
	a.mu.RLock()
	last := a.autoLastRun
	trayReady := a.trayReady
	a.mu.RUnlock()
	if last.IsZero() {
		if raw, err := a.store.GetSetting(context.Background(), "update.last_run"); err == nil && raw != "" {
			var unix int64
			fmt.Sscanf(raw, "%d", &unix)
			last = time.Unix(unix, 0)
		}
	}
	if !last.IsZero() && time.Since(last) < time.Duration(interval)*time.Hour {
		return
	}
	go func() {
		defer func() { recover() }()
		apps, err := a.CheckUpdates(false)
		if err != nil {
			return
		}
		count := 0
		for _, app := range apps {
			if app.UpdateAvailable {
				count++
			}
		}
		_ = a.store.SetSetting(context.Background(), "update.last_run", fmt.Sprintf("%d", time.Now().Unix()))
		a.emit("updates:checked", map[string]any{"count": count})
		if count > 0 && trayReady {
			setTrayTooltip(fmt.Sprintf("Komi Go · %d 个可用更新", count))
		}
	}()
}

// ---------------------------------------------------------------------------
// self update
// ---------------------------------------------------------------------------

func (a *App) selfUpdateRepo() string {
	repo, _ := a.store.GetSetting(context.Background(), "self_update.repo")
	return strings.TrimSpace(repo)
}

// CheckSelfUpdate asks the feed repo for a newer installer.
func (a *App) CheckSelfUpdate() (selfupdate.Info, error) {
	repo := a.selfUpdateRepo()
	if repo == "" {
		return selfupdate.Info{}, errors.New("请先在设置里填写自更新仓库（owner/repo）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := selfupdate.Check(ctx, a.clientFor("github.com"), repo, Version)
	if err != nil {
		return selfupdate.Info{}, errors.New(friendly(err))
	}
	a.mu.Lock()
	a.selfInfo = info
	a.mu.Unlock()
	return info, nil
}

// DownloadAndApplySelfUpdate downloads the installer, launches a detached
// updater script and quits so the binary can be replaced.
func (a *App) DownloadAndApplySelfUpdate() error {
	a.mu.Lock()
	info := a.selfInfo
	a.mu.Unlock()
	if !info.Available || info.URL == "" {
		return errors.New("没有可用的自更新")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	lastEmit := time.Now()
	path, err := selfupdate.Download(ctx, a.httpClient, info, a.layout.Downloads, func(done, total int64) {
		if time.Since(lastEmit) < 200*time.Millisecond && done < total {
			return
		}
		lastEmit = time.Now()
		a.emit("selfupdate:progress", map[string]any{"done": done, "total": total})
	})
	if err != nil {
		return err
	}
	if err := selfupdate.Launch(path, a.exePath()); err != nil {
		return fmt.Errorf("无法启动更新程序：%w", err)
	}
	a.emit("selfupdate:ready", path)
	go func() {
		time.Sleep(1200 * time.Millisecond)
		a.Quit()
	}()
	return nil
}

// VersionInfo is the tiny footer payload.
func (a *App) VersionInfo() map[string]any {
	return map[string]any{
		"version": Version,
		"data":    a.layout.Root,
		"exe":     a.exePath(),
	}
}

// OpenLogs opens the log directory.
func (a *App) OpenLogs() error {
	return platform.OpenFolder(a.layout.Logs)
}


