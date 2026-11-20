package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"komi-go/internal/auth"
	"komi-go/internal/catalog"
	"komi-go/internal/download"
	"komi-go/internal/github"
	"komi-go/internal/ledger"
	"komi-go/internal/platform"
	"komi-go/internal/selfupdate"
	"komi-go/internal/winreg"
)

// Version is shown in the UI and copied into diagnostics. It is a variable so
// the release build can inject it with -ldflags "-X main.Version=...".
var Version = "0.3.0"

// App is the only struct bound to the frontend. Every exported method becomes
// window.go.main.App.<Method> and must only take/return JSON-serialisable values.
type App struct {
	ctx        context.Context
	layout     platform.Layout
	store      *ledger.Store
	backend    *github.Backend
	dl         *download.Manager
	httpClient *http.Client

	mu         sync.RWMutex
	token      string
	envToken   string
	login      string
	clients    map[string]*github.Client
	views      map[string]catalog.RepoView
	jobApps    map[string]string
	lastImport *importBundle
	selfInfo   selfupdate.Info
	apiBase    string
	mirrorRaw  string

	quitting      bool
	trayReady     bool
	schedulerStop chan struct{}
	autoLastRun   time.Time
	startArgs     []string
	deepLinkSeen  string
}

// NewApp wires the object graph by hand; there is no DI container to learn.
func NewApp() (*App, error) {
	layout, err := platform.EnsureLayout()
	if err != nil {
		return nil, err
	}
	store, err := ledger.Open(layout.Database)
	if err != nil {
		return nil, err
	}
	a := &App{
		layout:    layout,
		store:     store,
		clients:   map[string]*github.Client{},
		views:     map[string]catalog.RepoView{},
		startArgs: os.Args[1:],
	}

	// Wrap the token in Credential Manager. GITHUB_TOKEN still wins so CI and
	// power users can override without touching the UI.
	a.envToken = platform.EnvToken()
	if a.envToken == "" {
		if tok, err := auth.LoadToken(); err == nil {
			a.token = tok
		} else {
			log.Printf("load token: %v", err)
		}
	}

	proxy, _ := store.GetSetting(context.Background(), "network.proxy")
	a.httpClient, err = platform.HTTPClient(proxy)
	if err != nil {
		a.httpClient, _ = platform.HTTPClient("")
	}

	a.dl = download.New(layout.Downloads, store, a.currentToken, a.emit)
	if hc, err := platform.HTTPClient(proxy); err == nil {
		a.dl.SetHTTPClient(hc)
	}
	a.backend = github.NewBackend(a.currentToken)
	a.backend.SetStore(store)
	backendClient := *a.httpClient
	backendClient.Timeout = 15 * time.Second
	a.backend.SetHTTPClient(&backendClient)
	a.ensureMirrorDefaults(store)
	a.applyMirrors()
	a.applyMirrorSelection()

	// Restore the persisted queue so a crash does not lose a 500 MB download.
	tasks, err := store.ListTasks(context.Background())
	if err == nil {
		a.dl.Restore(tasks)
	}
	_ = store.TrimCache(context.Background(), 64<<20)
	return a, nil
}

// startup runs once the webview is alive.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	_ = winreg.EnsureURLScheme(a.exePath())
	a.emit("app:ready", a.AuthState())
	go a.startScheduler()

	// A deep link may arrive as the first argument (double-click / protocol).
	if ref, ok := a.deepLinkFromArgs(a.startArgs); ok {
		a.emit("deeplink", ref)
	}
	// --tray is used by the autostart entry: boot silently.
	for _, arg := range a.startArgs {
		if strings.EqualFold(arg, "--tray") {
			go func() {
				time.Sleep(600 * time.Millisecond)
				a.hideWindow()
			}()
		}
	}
	a.startTray()
}

func (a *App) shutdown(ctx context.Context) {
	a.stopScheduler()
	a.stopTray()
	a.dl.Shutdown()
	if err := a.store.Close(); err != nil {
		log.Printf("close db: %v", err)
	}
}

func (a *App) emit(name string, payload any) {
	if a.ctx == nil {
		return
	}
	wruntime.EventsEmit(a.ctx, name, payload)
}

func (a *App) exePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func (a *App) currentToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" {
		return a.token
	}
	return a.envToken
}

func (a *App) signedIn() bool {
	return a.currentToken() != ""
}

func (a *App) clientFor(host string) *github.Client {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || host == "github.com" || host == "api.github.com" {
		host = "github.com"
	}
	a.mu.Lock()
	if c, ok := a.clients[host]; ok {
		a.mu.Unlock()
		return c
	}
	a.mu.Unlock()

	var c *github.Client
	if host == "github.com" {
		c = github.New(a.currentToken)
	} else {
		c = github.NewForgejo(host, a.currentToken)
	}
	c.SetStore(a.store)
	c.SetHTTPClient(a.httpClient)
	c.SetUnauthorizedHandler(a.onUnauthorized)
	a.mu.Lock()
	c.SetAPIBase(a.apiBase)
	a.clients[host] = c
	a.mu.Unlock()
	return c
}

// onUnauthorized is called from a request goroutine when GitHub answers 401.
// It drops the in-memory token and persisted credential exactly once.
func (a *App) onUnauthorized() {
	a.mu.Lock()
	if a.token == "" {
		a.mu.Unlock()
		return
	}
	a.token = ""
	a.login = ""
	a.mu.Unlock()
	_ = auth.DeleteToken()
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "auth:expired", "令牌已失效，请重新登录")
		wruntime.EventsEmit(a.ctx, "auth:changed", a.AuthState())
	}
}

func (a *App) applyMirrors() {
	raw, _ := a.store.GetSetting(context.Background(), "network.mirrors")
	list := parseMirrors(raw)
	a.dl.SetMirrors(list)
}

// applyMirrorSelection wires the selected mirror into downloads, GitHub API
// requests and README/page resource rewriting.
func (a *App) applyMirrorSelection() {
	ctx := context.Background()
	mode, _ := a.store.GetSetting(ctx, "network.mirrorSelected")
	if strings.TrimSpace(mode) == "" {
		mode = "auto"
	}
	raw, _ := a.store.GetSetting(ctx, "network.mirrors")
	mirrors := parseMirrors(raw)
	tpl := ""
	if mode != "auto" && mode != "direct" {
		for _, m := range mirrors {
			if m.Template == mode {
				tpl = mode
				break
			}
		}
	}
	a.dl.SetSelected(tpl)
	a.dl.SetAuto(mode == "auto")
	active := a.dl.ActiveMirror()
	if mode == "direct" {
		active = ""
	}
	apiBase := ""
	if a.currentToken() == "" {
		for _, m := range mirrors {
			if m.Template == active && m.API {
				apiBase = download.ApplyMirror(active, "https://api.github.com")
				break
			}
		}
	}
	a.mu.Lock()
	a.mirrorRaw = active
	a.apiBase = apiBase
	for _, c := range a.clients {
		c.SetAPIBase(apiBase)
	}
	a.mu.Unlock()
}

// ensureMirrorDefaults seeds the built-in accelerator list exactly once. A
// user who deliberately clears the list stays cleared because of the seeded flag.
func (a *App) ensureMirrorDefaults(store *ledger.Store) {
	seeded, _ := store.GetSetting(context.Background(), "network.mirrors.seeded")
	if seeded == "true" {
		return
	}
	raw, _ := store.GetSetting(context.Background(), "network.mirrors")
	if strings.TrimSpace(raw) == "" {
		_ = store.SetSetting(context.Background(), "network.mirrors", presetMirrorJSON())
	}
	_ = store.SetSetting(context.Background(), "network.mirrors.seeded", "true")
}

// ---------------------------------------------------------------------------
// auth
// ---------------------------------------------------------------------------

// AuthState is the payload the shell needs on boot.
func (a *App) AuthState() map[string]any {
	a.mu.Lock()
	token := a.token
	login := a.login
	a.mu.Unlock()
	if token == "" {
		token = a.envToken
	}
	if login == "" && token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		if name, err := a.clientFor("github.com").Login(ctx); err == nil {
			a.mu.Lock()
			a.login = name
			a.mu.Unlock()
			login = name
		}
		cancel()
	}
	clientID := a.oauthClientID()
	client := a.clientFor("github.com")
	rate := client.RateLimit()
	if rate.Limit == 0 || (!rate.Reset.IsZero() && !time.Now().Before(rate.Reset)) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if fresh, err := client.RefreshRateLimit(ctx); err == nil {
			rate = fresh
		}
		cancel()
	}
	autostart := winreg.AutostartEnabled()
	scheme := winreg.URLSchemeRegistered(a.exePath())
	return map[string]any{
		"version":         Version,
		"target":          catalog.CurrentTarget(),
		"downloadsDir":    a.layout.Downloads,
		"dataDir":         a.layout.Root,
		"appsDir":         a.layout.Apps,
		"logsDir":         a.layout.Logs,
		"signedIn":        token != "",
		"login":           login,
		"hasEnvToken":     a.envToken != "",
		"tokenPersisted":  a.token != "",
		"clientIdSet":     strings.TrimSpace(clientID) != "",
		"deviceFlowReady": strings.TrimSpace(clientID) != "",
		"autostart":       autostart,
		"urlScheme":       scheme,
		"tray":            a.isTrayReady(),
		"rateLimit":       rate,
		"elevated":        winreg.IsElevated(),
	}
}

// Profile returns avatar, display name and social counts for the settings
// profile header. Signed-out or unavailable accounts degrade to an empty map
// instead of blocking the settings page.
func (a *App) Profile() map[string]any {
	if a.currentToken() == "" {
		return map[string]any{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	p, err := a.clientFor("github.com").Profile(ctx)
	if err != nil {
		return map[string]any{"login": a.login, "error": friendly(err)}
	}
	return map[string]any{
		"login":       p.Login,
		"name":        p.Name,
		"avatarUrl":   p.AvatarURL,
		"publicRepos": p.PublicRepos,
		"followers":   p.Followers,
		"following":   p.Following,
	}
}

// inferAssetPlatform guesses a platform from a release asset filename when the
// release API did not attach an explicit platform.
func inferAssetPlatform(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".exe"), strings.HasSuffix(n, ".msi"), strings.HasSuffix(n, ".msix"),
		strings.Contains(n, "windows"), strings.Contains(n, "win64"), strings.Contains(n, "win32"):
		return "windows"
	case strings.HasSuffix(n, ".dmg"), strings.HasSuffix(n, ".pkg"),
		strings.Contains(n, "macos"), strings.Contains(n, "darwin"), strings.Contains(n, "osx"):
		return "macos"
	case strings.HasSuffix(n, ".deb"), strings.HasSuffix(n, ".rpm"), strings.HasSuffix(n, ".appimage"),
		strings.Contains(n, "linux"):
		return "linux"
	}
	return ""
}

// ProfileRepos lists the signed-in account's repositories that actually have
// a published Release. The profile page mirrors upstream Komi Store: only
// release-bearing repositories are shown.
func (a *App) ProfileRepos() ([]map[string]any, error) {
	if a.currentToken() == "" {
		return nil, errors.New("请先登录 GitHub")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := a.clientFor("github.com")
	repos, err := client.UserRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取仓库失败：%s", friendly(err))
	}

	results := make([]map[string]any, len(repos))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func(i int, repo github.UserRepo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			releases, _, err := client.Releases(ctx, repo.Owner.Login, repo.Name, 1, false)
			if err != nil || len(releases) == 0 {
				return
			}
			latest := releases[0]
			if latest.Draft {
				return
			}
			platforms := make([]string, 0, 3)
			seen := map[string]bool{}
			for _, asset := range latest.Assets {
				if asset.Junk {
					continue
				}
				platform := strings.ToLower(strings.TrimSpace(asset.Platform))
				if platform == "" {
					platform = inferAssetPlatform(asset.Name)
				}
				if platform == "" || seen[platform] {
					continue
				}
				seen[platform] = true
				platforms = append(platforms, platform)
			}
			published := latest.PublishedAt
			if published.IsZero() {
				published = latest.CreatedAt
			}
			results[i] = map[string]any{
				"owner":       repo.Owner.Login,
				"name":        repo.Name,
				"fullName":    repo.FullName,
				"description": repo.Description,
				"language":    repo.Language,
				"avatarUrl":   repo.Owner.AvatarURL,
				"stars":       repo.StargazersCount,
				"forks":       repo.ForksCount,
				"private":     repo.Private,
				"updatedAt":   repo.UpdatedAt.Format(time.RFC3339),
				"latestTag":   latest.TagName,
				"latestName":  latest.Name,
				"latestUrl":   latest.HTMLURL,
				"publishedAt": published.Format(time.RFC3339),
				"assetCount":  len(latest.Assets),
				"platforms":   platforms,
			}
		}(i, repo)
	}
	wg.Wait()

	out := make([]map[string]any, 0, len(results))
	for _, row := range results {
		if row != nil {
			out = append(out, row)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, _ := out[i]["publishedAt"].(string)
		right, _ := out[j]["publishedAt"].(string)
		return left > right
	})
	return out, nil
}

// SetToken validates a PAT, stores it in Credential Manager and returns the
// account name.
func (a *App) SetToken(raw string) (string, error) {
	tok := strings.TrimSpace(raw)
	if tok == "" {
		return "", errors.New("令牌为空")
	}
	if len(tok) < 20 || len(tok) > 512 {
		return "", errors.New("令牌长度异常")
	}
	a.mu.Lock()
	a.token = tok
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	login, err := a.clientFor("github.com").Login(ctx)
	if err != nil {
		a.mu.Lock()
		a.token = ""
		a.mu.Unlock()
		return "", fmt.Errorf("令牌校验失败：%s", friendly(err))
	}
	a.mu.Lock()
	a.login = login
	a.mu.Unlock()
	if err := auth.SaveToken(tok); err != nil {
		a.emit("auth:warning", "令牌已生效，但无法写入 Windows 凭据管理器："+err.Error())
	}
	a.emit("auth:changed", a.AuthState())
	return login, nil
}

// ClearToken forgets the credential in memory and in Credential Manager.
func (a *App) ClearToken() error {
	a.mu.Lock()
	a.token = ""
	a.login = ""
	a.mu.Unlock()
	err := auth.DeleteToken()
	a.emit("auth:changed", a.AuthState())
	if err != nil {
		return err
	}
	return nil
}

const defaultOAuthClientID = "Ov23liZEEZrWKyHzNrRF"

// oauthClientID returns the user override if present, otherwise the OAuth App
// bundled with Komi Go. A client ID is public; the device flow never ships a
// client secret.
func (a *App) oauthClientID() string {
	if id, _ := a.store.GetSetting(context.Background(), "github.client_id"); strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	return defaultOAuthClientID
}

// StartDeviceFlow begins GitHub's device authorization flow. The client id
// comes from settings so no secret ever ships with the app.
func (a *App) StartDeviceFlow() (github.DeviceCode, error) {
	clientID := a.oauthClientID()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.clientFor("github.com").StartDeviceFlow(ctx, strings.TrimSpace(clientID), "repo")
}

// PollDeviceFlow polls once; the frontend decides the interval. On success the
// token is validated, persisted and the account name returned.
func (a *App) PollDeviceFlow(deviceCode string) (map[string]any, error) {
	clientID := a.oauthClientID()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	token, pending, slowDown, err := a.clientFor("github.com").PollDeviceFlow(ctx, clientID, deviceCode)
	if err != nil {
		return nil, err
	}
	if pending {
		return map[string]any{"pending": true, "slowDown": slowDown}, nil
	}
	login, err := a.SetToken(token)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pending": false, "login": login, "signedIn": true}, nil
}

// ---------------------------------------------------------------------------
// misc
// ---------------------------------------------------------------------------

// OpenExternal opens only http(s) links in the default browser.
func (a *App) OpenExternal(raw string) error {
	return platform.OpenURL(raw)
}

// OpenFolder opens a known app folder.
func (a *App) OpenFolder(kind string) error {
	var dir string
	switch kind {
	case "downloads":
		dir = a.layout.Downloads
	case "apps":
		dir = a.layout.Apps
	case "logs":
		dir = a.layout.Logs
	case "data":
		dir = a.layout.Root
	default:
		return errors.New("未知目录")
	}
	return platform.OpenFolder(dir)
}

// Diagnostics is the text the user can paste into an issue. It never contains
// the token.
func (a *App) Diagnostics() string {
	ctx := context.Background()
	count, size, _ := a.store.CacheStats(ctx)
	settings, _ := a.store.AllSettings(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "Komi Go %s\n", Version)
	fmt.Fprintf(&b, "OS: %s/%s  elevated=%v\n", runtime.GOOS, runtime.GOARCH, winreg.IsElevated())
	fmt.Fprintf(&b, "Data: %s\n", a.layout.Root)
	fmt.Fprintf(&b, "Downloads: %s\n", a.layout.Downloads)
	fmt.Fprintf(&b, "DB cache: %d entries / %s\n", count, platform.HumanBytes(size))
	fmt.Fprintf(&b, "Signed in: %v  env token: %v\n", a.signedIn(), a.envToken != "")
	fmt.Fprintf(&b, "URL scheme: %v  autostart: %v  tray: %v\n", winreg.URLSchemeRegistered(a.exePath()), winreg.AutostartEnabled(), a.isTrayReady())
	rl := a.clientFor("github.com").RateLimit()
	fmt.Fprintf(&b, "Rate limit: %d/%d reset %s\n", rl.Remaining, rl.Limit, rl.Reset.Format(time.RFC3339))
	b.WriteString("Settings:\n")
	for k, v := range settings {
		if strings.Contains(k, "token") {
			continue
		}
		fmt.Fprintf(&b, "  %s=%s\n", k, v)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// friendly turns API errors into one short line the UI can show verbatim.
func friendly(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *github.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 401:
			return "登录已失效，请重新登录"
		case 403:
			if strings.Contains(strings.ToLower(apiErr.Message), "rate limit") {
				return "GitHub 限流了，请登录或稍后再试"
			}
		case 404:
			return "仓库或文件不存在"
		case 429:
			return "请求过于频繁，请稍后再试"
		}
		if apiErr.Message != "" {
			return apiErr.Message
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时，请检查网络或代理"
	}
	return err.Error()
}

func unixTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

func splitRepoKey(key string) (string, string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return key, ""
	}
	return parts[0], parts[1]
}

func displayName(owner, repo string) string {
	if repo == "" {
		return owner
	}
	return repo
}

func sanitizeID(s string) string {
	return platform.SanitizeFileName(strings.ReplaceAll(s, "#", "-"))
}

func updErrText(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *github.Error
	if errors.As(err, &apiErr) {
		return apiErr.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "检查超时"
	}
	return "检查失败：" + err.Error()
}

func (a *App) logPath() string {
	return filepath.Join(a.layout.Logs, "komi.log")
}
