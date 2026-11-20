// Package matcher implements the asset-matching and update-decision algorithms.
// Everything here is pure and unit-testable: no network, no database, no UI.
package matcher

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"komi-go/internal/catalog"
)

// ---------------------------------------------------------------------------
// name helpers
// ---------------------------------------------------------------------------

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// HasToken reports whether tok occurs in s delimited by non-word characters.
// Go's regexp (RE2) has no lookbehind, so boundaries are checked by hand.
// Treating '_' as a word character keeps "x86" from matching inside "x86_64".
func HasToken(s, tok string) bool {
	s = strings.ToLower(s)
	tok = strings.ToLower(tok)
	if tok == "" || len(tok) > len(s) {
		return false
	}
	for i := 0; ; {
		idx := strings.Index(s[i:], tok)
		if idx < 0 {
			return false
		}
		start := i + idx
		end := start + len(tok)
		okBefore := start == 0 || !isWordChar(rune(s[start-1]))
		okAfter := end >= len(s) || !isWordChar(rune(s[end]))
		if okBefore && okAfter {
			return true
		}
		i = start + 1
		if i >= len(s) {
			return false
		}
	}
}

func hasAnyToken(s string, toks []string) bool {
	for _, t := range toks {
		if HasToken(s, t) {
			return true
		}
	}
	return false
}

var (
	alpineArch   = []string{"amd64", "x86_64", "aarch64", "armv7", "ppc64le", "s390x", "riscv64", "loong64"}
	windowsHints = []string{"windows", "win64", "win32", "win"}
	linuxHints   = []string{"linux", "ubuntu", "debian", "fedora", "archlinux", "appimage"}
	archiveExts  = []string{".zip", ".7z", ".tar.gz", ".tgz", ".tar.xz"}
)

// isArchive reports whether the name is a plain archive (not a native installer).
func isArchive(low string) bool {
	for _, e := range archiveExts {
		if strings.HasSuffix(low, e) {
			return true
		}
	}
	return false
}

// PlatformOf classifies an asset by extension and, for archives, by name hints.
// Returns "" when the platform cannot be established.
func PlatformOf(name string) string {
	l := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasSuffix(l, ".apk"):
		// Alpine packages are also .apk but are Linux artifacts.
		if hasAnyToken(l, alpineArch) {
			return catalog.PlatformLinux
		}
		return catalog.PlatformAndroid
	case strings.HasSuffix(l, ".ipa"):
		return catalog.PlatformIOS
	case strings.HasSuffix(l, ".exe"), strings.HasSuffix(l, ".msi"), strings.HasSuffix(l, ".msix"):
		return catalog.PlatformWindows
	case strings.HasSuffix(l, ".dmg"), strings.HasSuffix(l, ".pkg"):
		return catalog.PlatformMacOS
	case strings.HasSuffix(l, ".deb"), strings.HasSuffix(l, ".rpm"),
		strings.HasSuffix(l, ".appimage"), strings.HasSuffix(l, ".snap"),
		strings.HasSuffix(l, ".flatpakref"), strings.HasSuffix(l, ".pkg.tar.zst"):
		return catalog.PlatformLinux
	}
	if isArchive(l) {
		if hasAnyToken(l, windowsHints) {
			return catalog.PlatformWindows
		}
		if hasAnyToken(l, linuxHints) {
			return catalog.PlatformLinux
		}
	}
	return ""
}

// ArchOf extracts the architecture a Windows asset targets.
// Order matters: arm64 must be tested before arm, amd64 before 386.
func ArchOf(name string) string {
	l := strings.ToLower(name)
	switch {
	case hasAnyToken(l, []string{"arm64", "aarch64", "arm64-v8a"}):
		return "arm64"
	case hasAnyToken(l, []string{"x86_64", "x64", "amd64"}):
		return "amd64"
	case hasAnyToken(l, []string{"x86", "i386", "i686", "386"}):
		return "386"
	case hasAnyToken(l, []string{"armeabi", "armv7", "armhf", "arm32", "arm"}):
		return "arm"
	case hasAnyToken(l, []string{"universal", "noarch", "all"}):
		return "universal"
	}
	return ""
}

// ArchScore rewards an exact arch match, then universal, then a compatible fallback.
func ArchScore(assetArch, targetArch string) int {
	if assetArch == targetArch {
		return 3
	}
	if assetArch == "universal" {
		return 2
	}
	if targetArch == "arm64" && assetArch == "arm" {
		return 1
	}
	if targetArch == "amd64" && assetArch == "386" {
		return 1
	}
	if assetArch == "" {
		return 1
	}
	return 0
}

// FlavorScore ranks stability: stable > release candidate > nightly > debug.
func FlavorScore(name string) int {
	l := strings.ToLower(name)
	switch {
	case hasAnyToken(l, []string{"debug", "snapshot", "canary", "dev"}):
		return 0
	case hasAnyToken(l, []string{"nightly"}):
		return 1
	case hasAnyToken(l, []string{"alpha", "beta", "rc", "preview", "pre"}):
		return 2
	default:
		return 3
	}
}

// ExtensionRank prefers native installers over plain archives.
func ExtensionRank(name string) int {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".exe"):
		return 100
	case strings.HasSuffix(l, ".msi"):
		return 95
	case strings.HasSuffix(l, ".msix"):
		return 92
	case strings.HasSuffix(l, ".zip"):
		return 70
	case strings.HasSuffix(l, ".7z"):
		return 55
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		return 30
	}
	return 0
}

var junkTokens = []string{
	"sha256", "sha512", "sha1", "checksum", "checksums", "signature", "sig", "asc",
	"blockmap", "source", "src", "symbols", "pdb", "debuginfo",
}

var junkExts = []string{".txt", ".json", ".yml", ".yaml", ".md", ".sig", ".asc", ".blockmap"}

// IsJunk filters out side-car files that look like installers but are not.
func IsJunk(name string) bool {
	l := strings.ToLower(name)
	for _, e := range junkExts {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	for _, t := range junkTokens {
		if hasAnyToken(l, []string{t}) {
			return true
		}
	}
	return false
}

// Score is the raw suitability score of a file name for this Windows target.
func Score(name string, target catalog.Target) int {
	score := ExtensionRank(name)
	if score == 0 {
		return -1
	}
	if PlatformOf(name) != target.OS {
		return -1
	}
	if IsJunk(name) {
		return -1
	}
	score += 18 * ArchScore(ArchOf(name), target.Arch)
	score += 6 * FlavorScore(name)
	if isArchive(strings.ToLower(name)) {
		score -= 15
	}
	return score
}

// Annotate fills Score/Installable/Recommended on the assets of every release,
// newest release first. It mutates the slice in place and returns the same value
// for call-site convenience.
func Annotate(releases []catalog.Release, target catalog.Target) []catalog.Release {
	for ri := range releases {
		rel := &releases[ri]
		var bestIdx = -1
		bestScore := -1
		rel.RecommendedAssetID = 0
		for ai := range rel.Assets {
			a := &rel.Assets[ai]
			a.Score = Score(a.Name, target)
			a.Platform = PlatformOf(a.Name)
			a.Junk = IsJunk(a.Name)
			a.Installable = a.Score >= 0
			a.Reason = reasonFor(a.Name, target, a.Score)
			if !a.Installable {
				continue
			}
			if a.Score > bestScore ||
				(a.Score == bestScore && bestIdx >= 0 && betterTieBreak(*a, rel.Assets[bestIdx])) {
				bestScore = a.Score
				bestIdx = ai
			}
		}
		if bestIdx >= 0 {
			rel.Assets[bestIdx].Recommended = true
			rel.RecommendedAssetID = rel.Assets[bestIdx].ID
		}
	}
	return releases
}

func betterTieBreak(a, b catalog.Asset) bool {
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	return strings.ToLower(a.Name) < strings.ToLower(b.Name)
}

func reasonFor(name string, target catalog.Target, score int) string {
	if score < 0 {
		if IsJunk(name) {
			return "校验/符号文件，不是安装包"
		}
		if p := PlatformOf(name); p != "" && p != target.OS {
			return "面向 " + p + "，当前是 " + target.OS
		}
		return "不是可安装的 Windows 包"
	}
	if arch := ArchOf(name); arch != "" && arch != "universal" && arch != target.Arch {
		return "架构为 " + arch + "，可能与本机不匹配"
	}
	return ""
}

// Choose returns the best installable asset for the target, or nil.
func Choose(assets []catalog.Asset, target catalog.Target) *catalog.Asset {
	var best *catalog.Asset
	bestScore := -1
	for i := range assets {
		a := &assets[i]
		score := Score(a.Name, target)
		if score < 0 {
			continue
		}
		if best == nil || score > bestScore ||
			(score == bestScore && betterTieBreak(*a, *best)) {
			best = a
			bestScore = score
		}
	}
	return best
}

// SortReleases orders releases newest first, drafts removed.
func SortReleases(releases []catalog.Release) []catalog.Release {
	out := make([]catalog.Release, 0, len(releases))
	for _, r := range releases {
		if r.Draft {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := out[i].PublishedAt, out[j].PublishedAt
		if ti.IsZero() {
			ti = out[i].CreatedAt
		}
		if tj.IsZero() {
			tj = out[j].CreatedAt
		}
		return ti.After(tj)
	})
	return out
}

// ---------------------------------------------------------------------------
// version math
// ---------------------------------------------------------------------------

var (
	preRe     = regexp.MustCompile(`(?i)(?:^|[-._])(alpha|beta|rc|pre|preview|snapshot|nightly)[._-]?\d*`)
	digitsRe  = regexp.MustCompile(`\d+`)
	hashTail  = regexp.MustCompile(`(?i)^v?\d+(?:[._-]\d+)*[-._][0-9a-f]{6,}$`)
	buildMeta = regexp.MustCompile(`\+.*$`)
)

// CompareVersion compares two tags numerically with prerelease awareness.
// Returns -1, 0 or 1. "v1.10" is correctly greater than "v1.9", and
// "v1.0.0-rc.2" is greater than "v1.0.0-rc.1".
func CompareVersion(a, b string) int {
	va, vb := normalizeTag(a), normalizeTag(b)
	if va == vb {
		return 0
	}
	na, pra := parseVersion(va)
	nb, prb := parseVersion(vb)
	for i := 0; i < len(na) || i < len(nb); i++ {
		var x, y int
		if i < len(na) {
			x = na[i]
		}
		if i < len(nb) {
			y = nb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	if pra == "" && prb == "" {
		return 0
	}
	if pra == "" {
		return 1
	}
	if prb == "" {
		return -1
	}
	return comparePrerelease(pra, prb)
}

func normalizeTag(tag string) string {
	t := strings.TrimSpace(tag)
	t = strings.TrimPrefix(t, "refs/tags/")
	t = strings.TrimPrefix(strings.TrimPrefix(t, "v"), "V")
	t = buildMeta.ReplaceAllString(t, "")
	return strings.TrimSpace(t)
}

// HasHashTail reports commit-hash style tags such as 2.0.9-1c19925b5.
// Those are not reconcilable with a clean 2.0.9.1 and must be tracked by
// release tag/timestamp instead of numeric comparison.
func HasHashTail(tag string) bool {
	return hashTail.MatchString(strings.TrimSpace(tag))
}

// parseVersion returns numeric segments and the prerelease segment (if any).
func parseVersion(tag string) ([]int, string) {
	t := normalizeTag(tag)
	pre := ""
	if loc := preRe.FindStringIndex(t); loc != nil {
		pre = t[loc[0]:]
		pre = strings.TrimLeft(pre, "-._")
		t = t[:loc[0]]
	}
	var nums []int
	for _, s := range digitsRe.FindAllString(t, -1) {
		if n, err := strconv.Atoi(s); err == nil {
			nums = append(nums, n)
		}
	}
	return nums, strings.ToLower(pre)
}

// comparePrerelease compares dot/hyphen separated identifiers using SemVer
// ordering: numeric identifiers compare numerically, numeric is lower than
// alphanumeric, and more identifiers win when the prefix is equal.
func comparePrerelease(a, b string) int {
	as := regexp.MustCompile(`[.\-_]`).Split(a, -1)
	bs := regexp.MustCompile(`[.\-_]`).Split(b, -1)
	for i := 0; i < len(as) || i < len(bs); i++ {
		if i >= len(as) {
			return -1
		}
		if i >= len(bs) {
			return 1
		}
		x, y := as[i], bs[i]
		if x == y {
			continue
		}
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn < yn {
				return -1
			}
			return 1
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		default:
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// asset identity: stem / glob
// ---------------------------------------------------------------------------

var (
	archTokenRe   = regexp.MustCompile(`(?i)(arm64-v8a|armeabi-v7a|arm64|aarch64|armeabi|armv7|armhf|x86_64|amd64|x64|x86|i386|i686|universal|noarch)`)
	flavorRe      = regexp.MustCompile(`(?i)(debug|release|snapshot|nightly|canary|alpha|beta|rc|preview|pre|fdroid|play|gms|huawei)`)
	versionRe     = regexp.MustCompile(`(?i)(?:v?\d+(?:[._-]\d+)*(?:[-._](?:alpha|beta|rc|pre)[._-]?\d*)?)`)
	buildRe       = regexp.MustCompile(`(?i)(?:\d{3,}|[0-9a-f]{6,})`)
	nonAlnumRe    = regexp.MustCompile(`[^a-z0-9]+`)
	buildSuffixRe = regexp.MustCompile(`\*[._-]?\d{3,}`)
	buildHexRe    = regexp.MustCompile(`(?i)\*[._-]?[0-9a-f]{6,}`)
)

// BaseStem strips version, arch and flavor words, leaving a stable family name.
func BaseStem(assetName string) string {
	stem := assetName
	if i := strings.LastIndex(stem, "."); i > 0 {
		stem = stem[:i]
	}
	stem = archTokenRe.ReplaceAllString(stem, " ")
	stem = flavorRe.ReplaceAllString(stem, " ")
	stem = versionRe.ReplaceAllString(stem, " ")
	stem = buildRe.ReplaceAllString(stem, " ")
	stem = nonAlnumRe.ReplaceAllString(stem, "-")
	return strings.Trim(strings.ToLower(stem), "-")
}

// TagGlob blanks the release version out of the asset name, so the same family
// can be recognised across versions: auth-v4.4.25-linux-amd64 + v4.4.25 ->
// auth-*-linux-amd64.
func TagGlob(assetName, tag string) string {
	if strings.TrimSpace(tag) == "" {
		return ""
	}
	stem := assetName
	ext := ""
	if i := strings.LastIndex(assetName, "."); i > 0 {
		stem = assetName[:i]
		ext = assetName[i:]
	}
	version := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(tag), "v"), "V")
	if m := regexp.MustCompile(`^\d+(?:[._-]\d+)*`).FindString(version); m != "" {
		version = m
	}
	nums := digitsRe.FindAllString(version, -1)
	if len(nums) == 0 {
		return ""
	}
	core := strings.Join(nums, `[._-]?`)
	re, err := regexp.Compile(`(?i)(^|[^a-z0-9])v?` + core + `([^a-z0-9]|$)`)
	if err != nil {
		return ""
	}
	replaced := re.ReplaceAllString(stem, "${1}*${2}")
	if !strings.Contains(replaced, "*") {
		return ""
	}
	replaced = buildSuffixRe.ReplaceAllString(replaced, "*")
	replaced = buildHexRe.ReplaceAllString(replaced, "*")
	replaced = strings.Trim(replaced, "-_. ")
	if replaced == "" {
		return ""
	}
	return strings.ToLower(replaced + ext)
}

// GlobMatches matches an asset name against a *-glob produced by TagGlob.
func GlobMatches(glob, assetName string) bool {
	if strings.TrimSpace(glob) == "" {
		return false
	}
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range glob {
		if r == '*' {
			b.WriteString(".*")
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(assetName)
}

// ---------------------------------------------------------------------------
// update verdict
// ---------------------------------------------------------------------------

// UpdateInput carries everything needed to decide whether an app should update.
type UpdateInput struct {
	InstalledTag         string
	InstalledAssetID     int64
	InstalledDigest      string
	InstalledSize        int64
	InstalledPublishedAt time.Time
	InstalledVersionCode int64

	CandidateTag         string
	CandidateAssetID     int64
	CandidateDigest      string
	CandidateSize        int64
	CandidatePublishedAt time.Time
	CandidateVersionCode int64

	SkippedTag         string
	SkippedPublishedAt time.Time
	Pinned             bool
	IncludePrerelease  bool
}

// DecideUpdate implements the seven-rule verdict plus three guards that real
// release feeds require: line separation, prerelease opt-in, size identity.
// The second return value is a diagnostic reason, never shown raw to the user.
func DecideUpdate(in UpdateInput) (bool, string) {
	if in.Pinned {
		return false, "pinned"
	}
	if !in.IncludePrerelease && IsPrereleaseTag(in.CandidateTag) {
		return false, "prerelease not enabled"
	}
	if in.InstalledTag != "" && in.CandidateTag != "" &&
		ReleaseLine(in.InstalledTag) != ReleaseLine(in.CandidateTag) &&
		ReleaseLine(in.InstalledTag) != "" && ReleaseLine(in.CandidateTag) != "" {
		return false, "different release line"
	}
	if in.InstalledDigest != "" && in.CandidateDigest != "" &&
		in.InstalledDigest == in.CandidateDigest {
		return false, "same bytes"
	}
	if in.InstalledAssetID != 0 && in.InstalledAssetID == in.CandidateAssetID {
		return false, "same asset"
	}
	if in.SkippedTag != "" && in.SkippedTag == in.CandidateTag {
		rebuilt := !in.CandidatePublishedAt.IsZero() &&
			in.CandidatePublishedAt.After(in.SkippedPublishedAt) &&
			in.CandidatePublishedAt.After(in.InstalledPublishedAt)
		if !rebuilt {
			return false, "skipped"
		}
	}
	if in.InstalledTag == in.CandidateTag && in.InstalledTag != "" {
		rebuilt := !in.CandidatePublishedAt.IsZero() &&
			in.CandidatePublishedAt.After(in.InstalledPublishedAt)
		if in.InstalledSize > 0 && in.CandidateSize > 0 && in.InstalledSize != in.CandidateSize {
			rebuilt = true
		}
		if rebuilt {
			return true, "same tag rebuilt"
		}
		return false, "same tag"
	}
	if in.InstalledVersionCode != 0 && in.InstalledVersionCode == in.CandidateVersionCode &&
		CompareVersion(in.CandidateTag, in.InstalledTag) == 0 {
		return false, "same versionCode"
	}
	if Reconcilable(in.CandidateTag, in.InstalledTag) {
		if cmp := CompareVersion(in.CandidateTag, in.InstalledTag); cmp > 0 {
			return true, "newer version"
		}
	}
	if !in.CandidatePublishedAt.IsZero() && in.CandidatePublishedAt.After(in.InstalledPublishedAt) {
		return true, "newer publish time"
	}
	return false, "not newer"
}
