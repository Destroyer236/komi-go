package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"komi-go/internal/catalog"
	"komi-go/internal/download"
	"komi-go/internal/github"
	"komi-go/internal/matcher"
	"komi-go/internal/platform"
	"komi-go/internal/winreg"
)

var settingKeys = []string{
	"theme", "update.auto", "update.interval", "update.prerelease",
	"update.fallback_older", "update.filter", "network.proxy", "network.mirrors",
	"network.mirrorSelected", "network.mirrors.seeded",
	"ui.close_to_tray", "ui.reduce_motion", "system.autostart", "github.client_id",
	"self_update.repo", "onboarding.done",
}

func presetMirrorJSON() string {
	data, _ := json.Marshal(download.PresetMirrors())
	return string(data)
}

// GetSettings returns persisted preferences with defaults filled in.
func (a *App) GetSettings() (map[string]string, error) {
	ctx := context.Background()
	out := map[string]string{
		"theme":                  "system",
		"update.auto":            "true",
		"update.interval":        "12",
		"update.prerelease":      "false",
		"update.fallback_older":  "true",
		"update.filter":          "",
		"network.proxy":          "",
		"network.mirrors":        presetMirrorJSON(),
		"network.mirrorSelected": "auto",
		"network.mirrors.seeded": "false",
		"ui.close_to_tray":       "true",
		"ui.reduce_motion":       "false",
		"system.autostart":       "false",
		"github.client_id":       "",
		"self_update.repo":       "",
		"onboarding.done":        "false",
	}
	for _, key := range settingKeys {
		v, err := a.store.GetSetting(ctx, key)
		if err != nil {
			return nil, err
		}
		if v != "" {
			out[key] = v
		}
	}
	a.mu.RLock()
	out["network.mirrorActive"] = a.mirrorRaw
	a.mu.RUnlock()
	return out, nil
}

// SetSetting validates and persists one preference, applying side effects
// immediately (proxy transport, mirrors, autostart).
func (a *App) SetSetting(key, value string) error {
	ctx := context.Background()
	value = strings.TrimSpace(value)
	switch key {
	case "theme":
		if value != "system" && value != "light" && value != "dark" {
			return errors.New("主题只能是 system/light/dark")
		}
	case "update.auto", "update.prerelease", "update.fallback_older",
		"ui.close_to_tray", "ui.reduce_motion", "system.autostart", "onboarding.done":
		if value != "true" && value != "false" {
			return errors.New("该设置只能是 true/false")
		}
	case "update.interval":
		n := 0
		if _, err := fmt.Sscanf(value, "%d", &n); err != nil || n < 1 || n > 168 {
			return errors.New("检查间隔需在 1–168 小时之间")
		}
		value = fmt.Sprintf("%d", n)
	case "update.filter":
		if err := matcher.AssetFilterError(value); err != nil {
			return err
		}
	case "network.proxy":
		if value != "" {
			if _, err := platform.HTTPClient(value); err != nil {
				return err
			}
		}
	case "network.mirrors":
		if value != "" {
			var list []download.Mirror
			if err := json.Unmarshal([]byte(value), &list); err != nil {
				return errors.New("镜像配置需为 JSON 数组：[{\"name\":\"\",\"template\":\"\"}]")
			}
			filtered := parseMirrors(value)
			if len(filtered) == 0 {
				return errors.New("没有有效的镜像：必须 https 且模板包含 {url}")
			}
			data, _ := json.Marshal(filtered)
			value = string(data)
		}
	case "network.mirrorSelected":
		if value != "auto" && value != "direct" {
			raw, _ := a.store.GetSetting(ctx, "network.mirrors")
			found := false
			for _, m := range parseMirrors(raw) {
				if m.Template == value {
					found = true
					break
				}
			}
			if !found {
				return errors.New("所选镜像不在当前镜像列表中")
			}
		}
	case "github.client_id":
		if len(value) > 100 {
			return errors.New("client_id 过长")
		}
		for _, r := range value {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return errors.New("client_id 只能包含字母、数字和 -")
			}
		}
	case "self_update.repo":
		if value != "" {
			ref, err := github.ParseRef(value)
			if err != nil || ref.Host != "github.com" {
				return errors.New("自更新仓库格式应为 owner/repo")
			}
		}
	default:
		return errors.New("未知设置项")
	}
	if err := a.store.SetSetting(ctx, key, value); err != nil {
		return err
	}
	switch key {
	case "network.proxy":
		a.rebuildHTTPClient(value)
	case "network.mirrors":
		a.applyMirrors()
		if sel, _ := a.store.GetSetting(ctx, "network.mirrorSelected"); sel != "" && sel != "auto" && sel != "direct" {
			found := false
			for _, m := range parseMirrors(value) {
				if m.Template == sel {
					found = true
					break
				}
			}
			if !found {
				_ = a.store.SetSetting(ctx, "network.mirrorSelected", "auto")
			}
		}
		a.applyMirrorSelection()
		_ = a.store.SetSetting(ctx, "network.mirrors.seeded", "true")
	case "network.mirrorSelected":
		a.applyMirrorSelection()
	case "system.autostart":
		_ = winreg.SetAutostart(a.exePath(), value == "true", true)
	}
	a.emit("settings:changed", map[string]string{key: value})
	return nil
}

func (a *App) boolSetting(key string, def bool) (bool, bool) {
	v, err := a.store.GetSetting(context.Background(), key)
	if err != nil || v == "" {
		return def, false
	}
	return v == "true", true
}

func parseMirrors(raw string) []download.Mirror {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var list []download.Mirror
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil
	}
	out := list[:0]
	for _, m := range list {
		t := strings.TrimSpace(m.Template)
		if !strings.HasPrefix(strings.ToLower(t), "https://") || !strings.Contains(t, "{url}") {
			continue
		}
		if m.Name == "" {
			m.Name = "镜像"
		}
		m.Template = t
		out = append(out, m)
	}
	return out
}

// MirrorPresets exposes the built-in accelerators to Settings.
func (a *App) MirrorPresets() []download.Mirror {
	return download.PresetMirrors()
}

// TestMirrors measures every configured and built-in mirror in parallel. The
// Settings page uses this to show latency and mark the fastest entry.
func (a *App) TestMirrors() []download.MirrorResult {
	raw, _ := a.store.GetSetting(context.Background(), "network.mirrors")
	list := parseMirrors(raw)
	seen := map[string]bool{}
	for _, m := range list {
		seen[m.Template] = true
	}
	for _, m := range download.PresetMirrors() {
		if !seen[m.Template] {
			list = append(list, m)
		}
	}
	if len(list) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.dl.TestMirrors(ctx, list, download.MirrorProbeURL)
}

func (a *App) rebuildHTTPClient(proxy string) {
	hc, err := platform.HTTPClient(proxy)
	if err != nil {
		a.emit("settings:error", err.Error())
		return
	}
	a.mu.Lock()
	a.httpClient = hc
	clients := make([]*github.Client, 0, len(a.clients))
	for _, c := range a.clients {
		clients = append(clients, c)
	}
	a.mu.Unlock()
	for _, c := range clients {
		c.SetHTTPClient(hc)
	}
	a.dl.SetHTTPClient(hc)
	if a.backend != nil {
		backendClient := *hc
		backendClient.Timeout = 15 * time.Second
		a.backend.SetHTTPClient(&backendClient)
	}
}

// SetProxy applies a proxy immediately without a full settings round-trip.
func (a *App) SetProxy(raw string) error {
	return a.SetSetting("network.proxy", raw)
}

// CacheInfo reports DB cache size and database size.
func (a *App) CacheInfo() map[string]any {
	ctx := context.Background()
	count, size, err := a.store.CacheStats(ctx)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var dbSize int64
	if info, err := os.Stat(a.layout.Database); err == nil {
		dbSize = info.Size()
	}
	var dlSize int64
	_ = filepath.WalkDir(a.layout.Downloads, func(path string, d os.DirEntry, err error) error {
		if err == nil && d != nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				dlSize += info.Size()
			}
		}
		return nil
	})
	return map[string]any{
		"entries":   count,
		"bytes":     size,
		"dbBytes":   dbSize,
		"downloads": dlSize,
	}
}

// ClearCache deletes cached HTTP payloads. kind is repo|releases|search|readme|all.
func (a *App) ClearCache(kind string) (int64, error) {
	var prefix string
	switch kind {
	case "", "all":
		prefix = ""
	case "repo", "releases", "search", "readme":
		prefix = kind + ":"
	default:
		return 0, errors.New("未知缓存类型")
	}
	n, err := a.store.ClearCache(context.Background(), prefix)
	if err == nil {
		a.mu.Lock()
		a.views = map[string]catalog.RepoView{}
		a.mu.Unlock()
	}
	return n, err
}

// catalogView is an alias so the map reset above stays readable.
type catalogView = struct{}

// PingAPI measures one lightweight API call; used by the settings page.
func (a *App) PingAPI() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := a.clientFor("github.com").Repo(ctx, "wailsapp", "wails")
	ms := time.Now().Sub(start).Milliseconds()
	if err != nil {
		return map[string]any{"ok": false, "ms": ms, "error": friendly(err)}, nil
	}
	return map[string]any{"ok": true, "ms": ms}, nil
}

// OnboardingState tells the first-run wizard whether to show.
func (a *App) OnboardingState() map[string]any {
	done, _ := a.store.GetSetting(context.Background(), "onboarding.done")
	return map[string]any{
		"needsOnboarding": done != "true",
		"theme":           firstNonEmpty(mustSetting(a.store, "theme"), "system"),
		"signedIn":        a.signedIn(),
		"autostart":       winreg.AutostartEnabled(),
		"closeToTray":     boolString(mustSetting(a.store, "ui.close_to_tray"), true),
	}
}

// CompleteOnboarding stores the three first-run choices.
func (a *App) CompleteOnboarding(theme string, autoCheck, closeToTray bool) error {
	if theme != "system" && theme != "light" && theme != "dark" {
		theme = "system"
	}
	pairs := map[string]string{
		"theme":            theme,
		"onboarding.done":  "true",
		"update.auto":      boolText(autoCheck),
		"ui.close_to_tray": boolText(closeToTray),
	}
	for k, v := range pairs {
		if err := a.store.SetSetting(context.Background(), k, v); err != nil {
			return err
		}
	}
	a.emit("settings:changed", pairs)
	return nil
}

func mustSetting(s interface {
	GetSetting(context.Context, string) (string, error)
}, key string) string {
	v, _ := s.GetSetting(context.Background(), key)
	return v
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func boolString(raw string, def bool) bool {
	if raw == "" {
		return def
	}
	return raw == "true"
}
