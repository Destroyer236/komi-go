package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"komi-go/internal/catalog"
	"komi-go/internal/github"
	"komi-go/internal/matcher"
	"komi-go/internal/platform"
)

// ---------------------------------------------------------------------------
// discovery / search
// ---------------------------------------------------------------------------

// SearchRepo resolves a pasted owner/repo, URL or komi:// deep link and loads
// the repository page.
func (a *App) SearchRepo(query string) (catalog.RepoView, error) {
	ref, err := github.ParseRef(query)
	if err != nil {
		return catalog.RepoView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return a.loadRepo(ctx, ref.Host, ref.Owner, ref.Name, false, ref.Tag)
}

// SearchRepositories powers the discover grid. sort is
// stars|updated|forks|best|releases. The Komi backend serves the default
// SearchPage carries paging metadata so the UI does not have to guess from a
// short page (filters can shrink a page while later pages still exist).
type SearchPage struct {
	Items    []catalog.RepoInfo `json:"items"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
	HasMore  bool               `json:"hasMore"`
}

const searchPageSize = 20

// GitHub search (backend index first, release-aware ordering); direct
// api.github.com remains for language filters, fork sorting and outages.
func (a *App) SearchRepositories(query string, page int, sort string, language string) ([]catalog.RepoInfo, error) {
	result, err := a.SearchPage(query, page, sort, language, "")
	return result.Items, err
}

// SearchPage is the paginated search entry point used by the UI. platform is
// "" for every installer-bearing project, or windows/macos/linux.
func (a *App) SearchPage(query string, page int, sort string, language string, platform string) (SearchPage, error) {
	raw := strings.TrimSpace(query)
	if len(raw) > 300 {
		return SearchPage{}, errors.New("搜索词过长")
	}
	if page <= 0 {
		page = 1
	}
	platform = normalizePlatform(platform)
	ctx, cancel := context.WithTimeout(context.Background(), 28*time.Second)
	defer cancel()

	var merged []catalog.RepoInfo
	var total int
	var backendErr error
	if raw != "" && a.backend != nil && sort != "forks" {
		backendSort := sort
		switch sort {
		case "", "stars":
			backendSort = "stars"
		case "best":
			backendSort = "relevance"
		case "releases":
			backendSort = "releases"
		case "updated":
			backendSort = "updated"
		}
		repos, hits, _, err := a.backend.Search(ctx, raw, page, searchPageSize, backendSort, platform, false)
		if err != nil {
			backendErr = err
			log.Printf("search backend err=%v page=%d platform=%q", err, page, platform)
			if !github.BackendShouldFallback(err) {
				return SearchPage{}, errors.New(friendly(err))
			}
		} else {
			merged = append(merged, repos...)
			total = hits
		}
	}

	// GitHub's own search knows about forks and freshly pushed derivative builds
	// that the aggregation index has not crawled yet. Merge it for completeness.
	if raw != "" && language == "" {
		repos, err := a.searchDirect(ctx, raw, page, sort, language, false)
		if err == nil {
			a.fillMissingPlatforms(ctx, repos)
			merged = mergeRepos(merged, repos)
			if len(merged) > total {
				total = len(merged)
			}
		} else if backendErr != nil {
			return SearchPage{}, err
		}
	}

	if len(merged) == 0 && backendErr == nil {
		return SearchPage{}, nil
	}
	// Source-only repositories are not apps: never show them.
	out := merged[:0]
	for _, r := range merged {
		if len(r.Platforms) == 0 {
			continue
		}
		if platform != "" && !hasPlatform(r, platform) {
			continue
		}
		out = append(out, r)
	}
	merged = out
	hasMore := page*searchPageSize < total || (total == 0 && len(merged) >= searchPageSize)
	return SearchPage{Items: merged, Total: total, Page: page, PageSize: searchPageSize, HasMore: hasMore}, nil
}

func hasPlatform(r catalog.RepoInfo, p string) bool {
	for _, v := range r.Platforms {
		if v == p {
			return true
		}
	}
	return false
}

func mergeRepos(a, b []catalog.RepoInfo) []catalog.RepoInfo {
	seen := make(map[string]int, len(a))
	for i, r := range a {
		seen[strings.ToLower(r.FullName)] = i
	}
	for _, r := range b {
		key := strings.ToLower(r.FullName)
		if i, ok := seen[key]; ok {
			// GitHub data is fresher for stars/forks; keep backend enrichment
			// (downloads, release date, platform flags) that GitHub search lacks.
			if len(a[i].Platforms) > 0 {
				r.Platforms = a[i].Platforms
			}
			if r.DownloadCount == 0 {
				r.DownloadCount = a[i].DownloadCount
			}
			if r.LatestReleaseAt.IsZero() {
				r.LatestReleaseAt = a[i].LatestReleaseAt
			}
			if r.LatestReleaseTag == "" {
				r.LatestReleaseTag = a[i].LatestReleaseTag
			}
			if r.DailyStars == 0 {
				r.DailyStars = a[i].DailyStars
			}
			a[i] = r
			continue
		}
		seen[key] = len(a)
		a = append(a, r)
	}
	return a
}

// fillMissingPlatforms inspects releases for GitHub-only search hits so
// source-only repositories can be filtered out and platform badges rendered.
// Only the first ten unknown repos are probed to keep search responsive.
func (a *App) fillMissingPlatforms(ctx context.Context, repos []catalog.RepoInfo) {
	var targets []int
	for i := range repos {
		if len(repos[i].Platforms) == 0 {
			targets = append(targets, i)
			if len(targets) >= 10 {
				break
			}
		}
	}
	if len(targets) == 0 {
		return
	}
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for _, i := range targets {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			repos[i].Platforms = a.platformsFor(cctx, repos[i].Owner, repos[i].Name)
			if len(repos[i].Platforms) == 0 {
				repos[i].PlatformNote = "仅提供源码"
			}
		}()
	}
	wg.Wait()
}

func (a *App) platformsFor(ctx context.Context, owner, repo string) []string {
	if a.backend != nil {
		if info, _, err := a.backend.Repo(ctx, owner, repo); err == nil && len(info.Platforms) > 0 {
			return info.Platforms
		}
	}
	releases, _, err := a.clientFor("github.com").Releases(ctx, owner, repo, 3, false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		for _, asset := range rel.Assets {
			if matcher.IsJunk(asset.Name) {
				continue
			}
			switch p := matcher.PlatformOf(asset.Name); p {
			case catalog.PlatformWindows, catalog.PlatformMacOS, catalog.PlatformLinux:
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func normalizePlatform(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case catalog.PlatformWindows:
		return catalog.PlatformWindows
	case catalog.PlatformMacOS, "mac", "darwin", "osx":
		return catalog.PlatformMacOS
	case catalog.PlatformLinux:
		return catalog.PlatformLinux
	default:
		return ""
	}
}

func filterByPlatform(list []catalog.RepoInfo, platform string) []catalog.RepoInfo {
	out := make([]catalog.RepoInfo, 0, len(list))
	for _, r := range list {
		for _, p := range r.Platforms {
			if p == platform {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// searchDirect is the api.github.com path. It is used when the aggregation
// backend cannot serve a query (language/fork sort) or is down.
func (a *App) searchDirect(ctx context.Context, raw string, page int, sort, language string, onlyInstallable bool) ([]catalog.RepoInfo, error) {
	q := raw
	if q == "" {
		q = defaultQuery()
	}
	if !strings.Contains(q, "archived:") && !strings.Contains(q, "is:") {
		q += " archived:false"
	}
	if !strings.Contains(q, "in:") && !strings.Contains(q, "topic:") {
		q += " in:name,description"
	}
	// Upstream searches forks as well; derivative/re-released builds often live
	// on a fork and are otherwise invisible to best-match search.
	if !strings.Contains(q, "fork:") {
		q += " fork:true"
	}
	repos, meta, err := a.clientFor("github.com").Search(ctx, q, page, 20, sort, language, false)
	if err != nil {
		return nil, errors.New(friendly(err))
	}
	if onlyInstallable && len(repos) > 0 {
		a.probeRepos(ctx, repos)
		out := repos[:0]
		for _, r := range repos {
			if r.PlatformReady {
				out = append(out, r)
			}
		}
		repos = out
	}
	_ = meta
	return repos, nil
}

// Discover returns curated feeds. kind: trending|fresh|windows|classic.
func (a *App) Discover(kind string, page int, onlyInstallable bool) ([]catalog.RepoInfo, error) {
	start := time.Now()
	log.Printf("discover start kind=%s page=%d only=%v", kind, page, onlyInstallable)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if a.backend != nil {
		repos, _, err := a.backend.Discover(ctx, kind, page, onlyInstallable)
		if err == nil {
			log.Printf("discover backend ok n=%d dur=%s", len(repos), time.Since(start).Round(time.Millisecond))
			return repos, nil
		}
		log.Printf("discover backend err=%v dur=%s", err, time.Since(start).Round(time.Millisecond))
		if !github.BackendShouldFallback(err) {
			return nil, errors.New(friendly(err))
		}
	}

	var q string
	var sort string
	switch kind {
	case "fresh":
		q = fmt.Sprintf("stars:>3 pushed:>%s archived:false", daysAgo(7))
		sort = "updated"
	case "windows":
		q = "windows in:name,description,readme stars:>10 archived:false"
		sort = "stars"
	case "classic":
		q = fmt.Sprintf("stars:>1000 created:<%s archived:false", daysAgo(180))
		sort = "stars"
	default:
		q = defaultQuery()
		sort = "stars"
	}
	return a.searchDirect(ctx, q, page, sort, "", onlyInstallable)
}

// CategoryCatalog returns the complete Windows Explore feed used by the
// category rail. It matches the upstream sidebar dataset instead of the
// currently visible discover page.
func (a *App) CategoryCatalog() ([]catalog.RepoInfo, error) {
	if a.backend == nil {
		return nil, errors.New("分类数据暂时不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repos, err := a.backend.CategoryFeed(ctx)
	if err != nil {
		return nil, errors.New(friendly(err))
	}
	return repos, nil
}

func defaultQuery() string {
	return fmt.Sprintf("stars:>60 pushed:>%s", daysAgo(30))
}

func daysAgo(n int) string {
	return time.Now().AddDate(0, 0, -n).Format("2006-01-02")
}

// probeRepos checks installability with a small worker pool. Results are cached
// for 24 h per repo so paging back and forth is free.
func (a *App) probeRepos(ctx context.Context, repos []catalog.RepoInfo) {
	if len(repos) == 0 {
		return
	}
	target := catalog.CurrentTarget()
	max := len(repos)
	if max > 14 {
		max = 14
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := 0; i < max; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ready, note := a.probeRepo(ctx, repos[i].SourceHost, repos[i].Owner, repos[i].Name, target)
			repos[i].PlatformReady = ready
			repos[i].PlatformNote = note
		}()
	}
	wg.Wait()
	for i := max; i < len(repos); i++ {
		repos[i].PlatformNote = "安装性未知"
	}
}

func (a *App) probeRepo(ctx context.Context, host, owner, repo string, target catalog.Target) (bool, string) {
	key := github.RepoKeyForHost(host, owner, repo)
	targetKey := target.OS + "/" + target.Arch
	if ready, detail, at, ok, _ := a.store.GetInstallable(ctx, key, targetKey); ok && time.Since(at) < 24*time.Hour {
		return ready, detail
	}
	if (host == "" || host == "github.com") && a.backend != nil {
		info, _, err := a.backend.Repo(ctx, owner, repo)
		if err == nil {
			ready, detail := info.PlatformReady, info.PlatformNote
			if !ready && detail == "" {
				detail = "没有 Windows 可安装资产"
			}
			_ = a.store.PutInstallable(context.Background(), key, targetKey, ready, detail)
			return ready, detail
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	releases, _, err := a.clientFor(host).Releases(cctx, owner, repo, 10, false)
	if err != nil {
		return false, "安装性未知"
	}
	ready, detail := judgeInstallable(releases, target)
	_ = a.store.PutInstallable(context.Background(), key, targetKey, ready, detail)
	return ready, detail
}

func judgeInstallable(releases []catalog.Release, target catalog.Target) (bool, string) {
	if len(releases) == 0 {
		return false, "没有发布任何 Release"
	}
	hasAssets := false
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		for _, asset := range rel.Assets {
			hasAssets = true
			if matcher.Score(asset.Name, target) >= 0 {
				return true, ""
			}
		}
	}
	if !hasAssets {
		return false, "Release 只有源码包，没有上传资产"
	}
	return false, "没有 Windows 可安装资产"
}

// ---------------------------------------------------------------------------
// repository page
// ---------------------------------------------------------------------------

// RefreshRepo bypasses the fresh cache when force is true.
func (a *App) RefreshRepo(host, owner, repo string, force bool) (catalog.RepoView, error) {
	if err := github.CheckOwnerRepo(owner, repo); err != nil {
		return catalog.RepoView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return a.loadRepo(ctx, host, owner, repo, force, "")
}

// RepoByKey loads a repo from its ledger key (owner/repo or host/owner/repo).
func (a *App) RepoByKey(key string, force bool) (catalog.RepoView, error) {
	host, owner, repo := github.SplitRepoKey(key)
	if owner == "" {
		return catalog.RepoView{}, errors.New("仓库键无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return a.loadRepo(ctx, host, owner, repo, force, "")
}

func (a *App) loadRepo(ctx context.Context, host, owner, repo string, force bool, tag string) (catalog.RepoView, error) {
	target := catalog.CurrentTarget()
	client := a.clientFor(host)
	key := github.RepoKeyForHost(host, owner, repo)

	isGitHub := host == "" || host == "github.com"
	useBackend := isGitHub && a.backend != nil
	var backendMeta github.FetchMeta
	var info catalog.RepoInfo
	var err error
	if useBackend {
		info, backendMeta, err = a.backend.Repo(ctx, owner, repo)
		if err != nil && github.BackendShouldFallback(err) {
			info, _, err = client.Repo(ctx, owner, repo)
			useBackend = false
		}
	} else {
		info, _, err = client.Repo(ctx, owner, repo)
	}
	if err != nil {
		return catalog.RepoView{}, errors.New(friendly(err))
	}
	info.SourceHost = host
	if info.SourceHost == "" {
		info.SourceHost = "github.com"
	}

	var releases []catalog.Release
	var rmeta github.FetchMeta
	if useBackend {
		releases, rmeta, err = a.backend.Releases(ctx, owner, repo, 30, force)
		if err != nil && github.BackendShouldFallback(err) {
			releases, rmeta, err = client.Releases(ctx, owner, repo, 30, force)
			useBackend = false
		}
	} else {
		releases, rmeta, err = client.Releases(ctx, owner, repo, 30, force)
	}
	if err != nil {
		return catalog.RepoView{}, errors.New(friendly(err))
	}
	if tag != "" {
		releases = matcher.SortReleases(releases)
	} else {
		releases = matcher.SortReleases(releases)
	}
	releases = matcher.Annotate(releases, target)

	if info.LatestReleaseTag == "" && len(releases) > 0 {
		info.LatestReleaseTag = releases[0].TagName
		info.LatestReleaseAt = releases[0].PublishedAt
	}
	ready, note := judgeInstallable(releases, target)
	if info.PlatformReady {
		ready, note = true, ""
	}
	info.PlatformReady = ready
	info.PlatformNote = note
	_ = a.store.PutInstallable(ctx, key, target.OS+"/"+target.Arch, ready, note)

	var readme string
	var rmeta2 github.FetchMeta
	var readmeErr error
	if useBackend {
		readme, rmeta2, readmeErr = a.backend.Readme(ctx, owner, repo)
		if readmeErr != nil && github.BackendShouldFallback(readmeErr) {
			readme, rmeta2, readmeErr = client.Readme(ctx, owner, repo)
		}
	} else {
		readme, rmeta2, readmeErr = client.Readme(ctx, owner, repo)
	}
	if len(readme) > 200_000 {
		readme = readme[:200_000] + "\n\n…（README 过长，已截断）"
	}
	view := catalog.RepoView{
		RepoInfo:       info,
		Target:         target,
		Releases:       releases,
		Stale:          rmeta.Stale || backendMeta.Stale,
		CachedAt:       rmeta.FetchedAt,
		RateLimit:      client.RateLimit(),
		Readme:         readme,
		ReadmeStale:    rmeta2.Stale,
		ReadmeCachedAt: rmeta2.FetchedAt,
		IssuesURL:      strings.TrimRight(info.HTMLURL, "/") + "/issues",
	}
	if readmeErr != nil {
		view.ReadmeError = friendly(readmeErr)
	}
	_ = a.store.SaveReleases(ctx, key, releases, "")

	a.mu.Lock()
	a.views[key] = view
	a.mu.Unlock()
	return view, nil
}

// rememberView is used by tests/other files.
func (a *App) cachedView(key string) (catalog.RepoView, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.views[key]
	return v, ok
}

// ---------------------------------------------------------------------------
// asset selection
// ---------------------------------------------------------------------------

// SelectAsset re-runs the matcher for a specific release with user preferences
// (prerelease / filter). It returns the recommended asset index and siblings so
// the UI can show "2/3 个候选".
func (a *App) SelectAsset(host, owner, repo, tag, filter string, includePre bool) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	releases, _, err := a.clientFor(host).Releases(ctx, owner, repo, 30, false)
	if err != nil {
		return nil, errors.New(friendly(err))
	}
	target := catalog.CurrentTarget()
	releases = matcher.SortReleases(releases)
	var picked catalog.Release
	for _, rel := range releases {
		if rel.TagName == tag {
			picked = rel
			break
		}
	}
	if picked.TagName == "" {
		return nil, errors.New("找不到这个 Release")
	}
	sel := matcher.SelectRelease([]catalog.Release{picked}, target, matcher.AppIdentity{}, includePre, false, filter)
	if sel.Asset == nil || sel.Asset.Name == "" {
		return nil, errors.New("该 Release 没有适用于本机的资产")
	}
	return map[string]any{
		"tag":         picked.TagName,
		"asset":       sel.Asset,
		"pickedIndex": sel.PickedIndex,
		"siblings":    sel.SiblingCount,
	}, nil
}

// OpenRepoPage opens the repo in the browser.
func (a *App) OpenRepoPage(host, owner, repo string) error {
	if err := github.CheckOwnerRepo(owner, repo); err != nil {
		return err
	}
	if strings.TrimSpace(host) == "" {
		host = "github.com"
	}
	return platform.OpenURL("https://" + host + "/" + owner + "/" + repo)
}
