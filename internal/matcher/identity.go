// Release lines, monorepo ownership and release selection.
//
// A monorepo can publish auth-v4.4.25 and web-v1.2 from the same repository. A
// naive "latest release" check would offer the web build to an auth user, so
// every update decision is scoped to the installed asset family and its line.
package matcher

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"komi-go/internal/catalog"
)

var (
	firstDigitRe = regexp.MustCompile(`\d`)
	preTagRe     = regexp.MustCompile(`(?i)(alpha|beta|rc|pre|preview|snapshot|nightly|canary|dev)`)
	nestedQuant  = regexp.MustCompile(`\([^)]*[+*][^)]*\)[+*]`)
)

// ReleaseLine extracts the product line from a tag:
//
//	auth-v4.4.25  -> "auth"
//	web-v1.2      -> "web"
//	v1.2.3        -> ""      (no line prefix)
//	nightly-2026  -> "nightly"
func ReleaseLine(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	t = strings.TrimPrefix(t, "v")
	loc := firstDigitRe.FindStringIndex(t)
	if loc == nil {
		return strings.Trim(t, "-_. ")
	}
	prefix := strings.Trim(t[:loc[0]], "-_. ")
	// "app-v" -> "app": the v belongs to the version, not the line.
	prefix = strings.TrimSuffix(prefix, "v")
	prefix = strings.Trim(prefix, "-_. ")
	return prefix
}

// SameLine reports whether two tags belong to the same product line.
func SameLine(a, b string) bool {
	return ReleaseLine(a) == ReleaseLine(b)
}

// IsPrereleaseTag looks at the tag, not the release flag. GitHub releases
// frequently have prerelease=false with an -rc1 tag.
func IsPrereleaseTag(tag string) bool {
	return preTagRe.MatchString(tag)
}

// VersionScheme classifies a tag so callers know whether comparing is honest.
func VersionScheme(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	if strings.Contains(t, "nightly") || strings.Contains(t, "canary") {
		return "nightly"
	}
	nums := digitsRe.FindAllString(t, -1)
	if len(nums) >= 3 {
		if n, err := strconv.Atoi(nums[0]); err == nil && n >= 1900 && n <= 2200 {
			return "date"
		}
	}
	if strings.Contains(t, ".") && len(nums) >= 2 {
		return "semver"
	}
	if len(nums) == 1 {
		return "build"
	}
	return "unknown"
}

// Reconcilable reports whether two tags can be compared with version math.
func Reconcilable(a, b string) bool {
	if HasHashTail(a) || HasHashTail(b) {
		return false
	}
	sa, sb := VersionScheme(a), VersionScheme(b)
	if sa == "unknown" || sb == "unknown" {
		return false
	}
	if sa == "nightly" || sb == "nightly" {
		return sa == sb
	}
	if sa == "date" || sb == "date" {
		return sa == sb
	}
	return true
}

// AppIdentity is the subset of an installed app needed to recognise its asset
// family. It is a plain struct so matcher stays free of database types.
type AppIdentity struct {
	AssetName  string // installed asset file name
	AssetGlob  string // learned glob, e.g. auth-*-windows-amd64.exe
	Slug       string // app slug / package tail, e.g. "auth"
	VersionTag string // installed release tag
}

// BelongsToApp decides whether an asset is the same app as the tracked record.
// Order: learned glob -> exact stem -> slug in name.
func BelongsToApp(assetName string, app AppIdentity) bool {
	name := strings.TrimSpace(assetName)
	if name == "" {
		return false
	}
	if app.AssetGlob != "" && GlobMatches(app.AssetGlob, name) {
		return true
	}
	if app.AssetName != "" {
		stem := BaseStem(app.AssetName)
		if stem != "" && BaseStem(name) == stem {
			return true
		}
	}
	if slug := strings.ToLower(strings.TrimSpace(app.Slug)); len(slug) >= 3 {
		low := strings.ToLower(name)
		if HasToken(low, slug) || strings.Contains(low, slug) {
			return true
		}
	}
	return false
}

// AssetFilterError validates a user-supplied regex. Go's RE2 has linear time
// complexity, but the handbook still limits pattern length and rejects nested
// quantifiers so the rule is portable to other engines.
func AssetFilterError(filter string) error {
	f := strings.TrimSpace(filter)
	if f == "" {
		return nil
	}
	if len(f) > 200 {
		return fmt.Errorf("资产过滤规则过长（最多 200 字符）")
	}
	if nestedQuant.MatchString(f) {
		return fmt.Errorf("资产过滤规则里不允许嵌套量词")
	}
	if _, err := regexp.Compile(`(?i)` + f); err != nil {
		return fmt.Errorf("资产过滤规则不是合法的正则：%w", err)
	}
	return nil
}

// FilterMatches applies an optional user asset filter.
func FilterMatches(filter, assetName string) bool {
	f := strings.TrimSpace(filter)
	if f == "" {
		return true
	}
	re, err := regexp.Compile(`(?i)` + f)
	if err != nil {
		return true // invalid filters are rejected before being stored
	}
	return re.MatchString(assetName)
}

// Selection is the result of resolving an app against a release window.
type Selection struct {
	Release      catalog.Release
	Asset        *catalog.Asset
	PickedIndex  int // index inside Release.Assets
	SiblingCount int // installable assets in this release
	FamilyMatch  bool
}

// SelectRelease picks the release + asset for an installed app.
//
// includePre     also accept prerelease releases and prerelease tags
// fallbackOlder  walk older releases when the newest one has no asset for this app
// filter         optional asset regex
func SelectRelease(releases []catalog.Release, target catalog.Target, app AppIdentity, includePre, fallbackOlder bool, filter string) Selection {
	sorted := SortReleases(releases)
	if len(sorted) == 0 {
		return Selection{}
	}
	instLine := ReleaseLine(app.VersionTag)
	hasIdentity := app.AssetName != "" || app.AssetGlob != "" || app.Slug != ""

	for ri, rel := range sorted {
		if !includePre && (rel.Prerelease || IsPrereleaseTag(rel.TagName)) {
			continue
		}
		if instLine != "" && ReleaseLine(rel.TagName) != "" && ReleaseLine(rel.TagName) != instLine {
			continue
		}

		var candidates []int
		for ai := range rel.Assets {
			a := rel.Assets[ai]
			if Score(a.Name, target) < 0 || !FilterMatches(filter, a.Name) {
				continue
			}
			if hasIdentity && !BelongsToApp(a.Name, app) {
				continue
			}
			candidates = append(candidates, ai)
		}
		if len(candidates) > 0 {
			best := -1
			bestScore := -1
			for _, ai := range candidates {
				s := Score(rel.Assets[ai].Name, target)
				if best < 0 || s > bestScore ||
					(s == bestScore && betterTieBreak(rel.Assets[ai], rel.Assets[best])) {
					best, bestScore = ai, s
				}
			}
			return Selection{
				Release:      rel,
				Asset:        &rel.Assets[best],
				PickedIndex:  best,
				SiblingCount: len(candidates),
				FamilyMatch:  true,
			}
		}
		if !hasIdentity {
			// Brand-new tracking record: the newest release with any installable
			// asset is the natural starting point.
			if best := Choose(rel.Assets, target); best != nil {
				return Selection{
					Release:      rel,
					Asset:        best,
					PickedIndex:  indexOfAsset(rel.Assets, best.ID),
					SiblingCount: countInstallable(rel.Assets, target),
				}
			}
		}
		if !fallbackOlder {
			break
		}
		_ = ri
	}
	return Selection{}
}

func indexOfAsset(assets []catalog.Asset, id int64) int {
	for i := range assets {
		if assets[i].ID == id {
			return i
		}
	}
	return 0
}

func countInstallable(assets []catalog.Asset, target catalog.Target) int {
	n := 0
	for _, a := range assets {
		if Score(a.Name, target) >= 0 {
			n++
		}
	}
	return n
}
