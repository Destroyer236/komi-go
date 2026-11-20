package matcher

import (
	"testing"
	"time"

	"komi-go/internal/catalog"
)

func TestPlatformOf(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"App-1.2-x64-setup.exe", catalog.PlatformWindows},
		{"App_1.2_x64.msi", catalog.PlatformWindows},
		{"app-universal.apk", catalog.PlatformAndroid},
		{"tool-1.0-amd64.apk", catalog.PlatformLinux}, // Alpine package
		{"App-1.0-arm64.dmg", catalog.PlatformMacOS},
		{"app_1.0_amd64.deb", catalog.PlatformLinux},
		{"tool-windows-x64.zip", catalog.PlatformWindows},
		{"tool-linux-x64.zip", catalog.PlatformLinux},
		{"README.md", ""},
	}
	for _, c := range cases {
		if got := PlatformOf(c.name); got != c.want {
			t.Errorf("PlatformOf(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestArchOfBoundaries(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"app-arm64-v8a-release.apk", "arm64"},
		{"app-aarch64.zip", "arm64"},
		{"App-1.5.2-x64.exe", "amd64"},
		{"app-x86_64.zip", "amd64"},
		{"app-i686.zip", "386"},
		{"app-armeabi-v7a.apk", "arm"},
		{"app-universal.apk", "universal"},
		{"app-noarch.rpm", "universal"},
		{"app-1.0.exe", ""},
	}
	for _, c := range cases {
		if got := ArchOf(c.name); got != c.want {
			t.Errorf("ArchOf(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	// x86 must not match inside x86_64.
	if ArchOf("app-x86_64.exe") == "386" {
		t.Fatal("x86_64 wrongly classified as 386")
	}
	if ArchOf("app-arm64.exe") == "arm" {
		t.Fatal("arm64 wrongly classified as arm")
	}
}

func TestChoosePrefersInstallerAndArch(t *testing.T) {
	target := catalog.Target{OS: catalog.PlatformWindows, Arch: "amd64"}
	assets := []catalog.Asset{
		{ID: 1, Name: "App-1.0-arm64.exe", Size: 90},
		{ID: 2, Name: "App-1.0-x64.exe", Size: 80},
		{ID: 3, Name: "App-1.0-x64.zip", Size: 200},
		{ID: 4, Name: "SHA256SUMS.txt", Size: 1},
		{ID: 5, Name: "App-1.0-linux-x64.tar.gz", Size: 50},
	}
	got := Choose(assets, target)
	if got == nil || got.ID != 2 {
		t.Fatalf("Choose = %+v, want id 2", got)
	}
}

func TestAnnotateMarksRecommendation(t *testing.T) {
	target := catalog.Target{OS: catalog.PlatformWindows, Arch: "amd64"}
	releases := []catalog.Release{
		{ID: 10, TagName: "v1.2.0", Assets: []catalog.Asset{
			{ID: 100, Name: "App-1.2.0-x64-setup.exe", Size: 10},
			{ID: 101, Name: "App-1.2.0-arm64-setup.exe", Size: 10},
		}},
	}
	Annotate(releases, target)
	if releases[0].RecommendedAssetID != 100 {
		t.Fatalf("recommended = %d, want 100", releases[0].RecommendedAssetID)
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.10.0", "v1.9.0", 1},
		{"1.2.3", "v1.2.3", 0},
		{"v2.0.0-beta.1", "v2.0.0", -1},
		{"v2.0.0-rc1", "v2.0.0-beta9", 1},
		{"release-1.2", "v1.2.0", 0},
		{"2026.10.25", "2026.9.30", 1},
		{"v1.0.0-rc.10", "v1.0.0-rc.2", 1},
		{"v1.0.0-beta.2", "v1.0.0-beta.10", -1},
		{"v1.0.0+build.7", "1.0.0", 0},
		{"v1.0.0-rc1", "v1.0.0", -1},
		{"refs/tags/v2.3", "V2.3.0", 0},
	}
	for _, c := range cases {
		if got := CompareVersion(c.a, c.b); got != c.want {
			t.Errorf("CompareVersion(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestTagGlobAndMatch(t *testing.T) {
	g := TagGlob("auth-v4.4.25-linux-amd64.tar.gz", "v4.4.25")
	if g == "" {
		t.Fatal("empty glob")
	}
	if !GlobMatches(g, "auth-v4.4.26-linux-amd64.tar.gz") {
		t.Fatalf("glob %q did not match next version", g)
	}
	if GlobMatches(g, "web-v4.4.26-linux-amd64.tar.gz") {
		t.Fatalf("glob %q leaked across release lines", g)
	}
}

func TestBaseStemGroupsFamily(t *testing.T) {
	a := BaseStem("App-1.2.3-x64-setup.exe")
	b := BaseStem("App-1.2.4-arm64-setup.exe")
	if a != b {
		t.Fatalf("stem mismatch: %q vs %q", a, b)
	}
}

func TestDecideUpdate(t *testing.T) {
	now := time.Now()
	old := now.Add(-24 * time.Hour)

	base := UpdateInput{
		InstalledTag:         "v1.0.0",
		InstalledAssetID:     11,
		InstalledDigest:      "sha256:aaa",
		InstalledPublishedAt: old,
		CandidateTag:         "v1.1.0",
		CandidateAssetID:     12,
		CandidateDigest:      "sha256:bbb",
		CandidatePublishedAt: now,
	}

	if u, _ := DecideUpdate(base); !u {
		t.Fatal("newer version not detected")
	}

	// Same bytes beats a newer tag.
	same := base
	same.CandidateDigest = "sha256:aaa"
	if u, r := DecideUpdate(same); u {
		t.Fatalf("same digest triggered update (%s)", r)
	}

	// Same tag, rebuilt later -> update.
	rebuilt := base
	rebuilt.CandidateTag = "v1.0.0"
	rebuilt.CandidatePublishedAt = now
	if u, r := DecideUpdate(rebuilt); !u {
		t.Fatalf("same tag rebuild missed (%s)", r)
	}

	// Skipped tag stays skipped unless rebuilt.
	skipped := base
	skipped.CandidateTag = "v1.0.0"
	skipped.SkippedTag = "v1.0.0"
	skipped.SkippedPublishedAt = now
	skipped.CandidatePublishedAt = old
	if u, r := DecideUpdate(skipped); u {
		t.Fatalf("skipped tag updated (%s)", r)
	}

	// Pinned never updates.
	pinned := base
	pinned.Pinned = true
	if u, _ := DecideUpdate(pinned); u {
		t.Fatal("pinned updated")
	}
}
