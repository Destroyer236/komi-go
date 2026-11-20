// Package catalog holds the domain models shared by the network, matching,
// download and ledger layers. It has no dependencies on Wails or SQLite so it
// can be unit-tested in isolation.
package catalog

import (
	"runtime"
	"time"
)

// Platform identifiers used across the app. Only Windows ships in v1, the
// others exist so that asset classification and filtering stay honest.
const (
	PlatformWindows = "windows"
	PlatformLinux   = "linux"
	PlatformMacOS   = "macos"
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
)

// Target describes the machine we are picking assets for.
type Target struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// CurrentTarget reports the host we are running on. The product is Windows-only
// but the arch is genuinely detected so ARM64 machines get the right binary.
func CurrentTarget() Target {
	arch := runtime.GOARCH
	switch arch {
	case "amd64", "arm64", "386", "arm":
	default:
		arch = "amd64"
	}
	return Target{OS: PlatformWindows, Arch: arch}
}

// Asset is one downloadable file attached to a release.
type Asset struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	ContentType   string `json:"contentType"`
	DownloadURL   string `json:"downloadUrl"`
	APIURL        string `json:"apiUrl,omitempty"`
	Digest        string `json:"digest,omitempty"`
	DownloadCount int64  `json:"downloadCount"`

	// Filled in by the matcher for the current target.
	Score       int    `json:"score,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
	Installable bool   `json:"installable"`
	Reason      string `json:"reason,omitempty"`
	// Platform is the OS this asset targets (windows/macos/linux/...). Junk
	// marks checksums, signatures and symbol files that are not packages.
	Platform string `json:"platform,omitempty"`
	Junk     bool   `json:"junk,omitempty"`
}

// Release mirrors the parts of a GitHub/Forgejo release we actually use.
type Release struct {
	ID                 int64     `json:"id"`
	TagName            string    `json:"tagName"`
	Name               string    `json:"name"`
	Body               string    `json:"body"`
	Draft              bool      `json:"draft"`
	Prerelease         bool      `json:"prerelease"`
	PublishedAt        time.Time `json:"publishedAt"`
	CreatedAt          time.Time `json:"createdAt"`
	HTMLURL            string    `json:"htmlUrl"`
	Assets             []Asset   `json:"assets"`
	RecommendedAssetID int64     `json:"recommendedAssetId"`
}

// RepoInfo is the repository header shown in the UI.
type RepoInfo struct {
	Owner         string    `json:"owner"`
	Name          string    `json:"name"`
	FullName      string    `json:"fullName"`
	Description   string    `json:"description"`
	Stars         int       `json:"stars"`
	Forks         int       `json:"forks"`
	OpenIssues    int       `json:"openIssues"`
	DefaultBranch string    `json:"defaultBranch"`
	AvatarURL     string    `json:"avatarUrl"`
	HTMLURL       string    `json:"htmlUrl"`
	Topics        []string  `json:"topics"`
	License       string    `json:"license"`
	Archived      bool      `json:"archived"`
	UpdatedAt     time.Time `json:"updatedAt"`
	SourceHost    string    `json:"sourceHost,omitempty"`

	// Enrichment from the Komi backend (GitHub only). These are optional so a
	// direct GitHub/Forgejo response still serialises cleanly.
	Language         string    `json:"language,omitempty"`
	LatestReleaseTag string    `json:"latestReleaseTag,omitempty"`
	LatestReleaseAt  time.Time `json:"latestReleaseAt,omitempty"`
	DownloadCount    int64     `json:"downloadCount,omitempty"`
	DailyStars       int       `json:"dailyStars,omitempty"`
	TrendingScore    float64   `json:"trendingScore,omitempty"`
	PopularityScore  float64   `json:"popularityScore,omitempty"`
	TopicCodes       []string  `json:"topicCodes,omitempty"`

	// PlatformReady is set by the installability probe; PlatformNote explains a
	// negative result ("没有 .exe/.msi 资产").
	PlatformReady bool   `json:"platformReady"`
	PlatformNote  string `json:"platformNote,omitempty"`
	// Platforms lists every desktop OS with a downloadable installer. It is
	// empty for source-only repositories, which the store hides.
	Platforms []string `json:"platforms,omitempty"`
}

// RateLimit is a snapshot of the API budget, surfaced in the UI footer so the
// user understands why a refresh was refused.
type RateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
}

// RepoView is the single payload the frontend renders a repository page from.
type RepoView struct {
	RepoInfo
	Target    Target    `json:"target"`
	Releases  []Release `json:"releases"`
	Stale     bool      `json:"stale"`
	CachedAt  time.Time `json:"cachedAt"`
	RateLimit RateLimit `json:"rateLimit"`

	// README is raw markdown; the frontend renders it. ReadmeError is set when
	// the fetch failed but the rest of the page is still usable.
	Readme         string    `json:"readme"`
	ReadmeStale    bool      `json:"readmeStale"`
	ReadmeCachedAt time.Time `json:"readmeCachedAt"`
	ReadmeError    string    `json:"readmeError,omitempty"`
	IssuesURL      string    `json:"issuesUrl"`
}
