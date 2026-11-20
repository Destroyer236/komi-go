package matcher

import (
	"testing"
	"time"

	"komi-go/internal/catalog"
)

func TestReleaseLine(t *testing.T) {
	cases := map[string]string{
		"auth-v4.4.25":      "auth",
		"web-v1.2":          "web",
		"v1.2.3":            "",
		"V2.0.1":            "",
		"app-v1.0":          "app",
		"nightly-20260101":  "nightly",
		"desktop-v3":        "desktop",
		"release-2026.10.1": "release",
		"latest":            "latest",
	}
	for tag, want := range cases {
		if got := ReleaseLine(tag); got != want {
			t.Errorf("ReleaseLine(%q)=%q want %q", tag, got, want)
		}
	}
	if !SameLine("auth-v4.4.25", "auth-v4.4.26") {
		t.Error("same auth line not recognised")
	}
	if SameLine("auth-v4.4.25", "web-v1.0") {
		t.Error("auth and web treated as one line")
	}
}

func TestPrereleaseAndScheme(t *testing.T) {
	for _, tag := range []string{"v1.2.0-rc1", "2.0 beta 3", "nightly-20260101", "app-v3.0-alpha"} {
		if !IsPrereleaseTag(tag) {
			t.Errorf("%q should be a prerelease tag", tag)
		}
	}
	if IsPrereleaseTag("v1.2.3") {
		t.Error("stable tag flagged prerelease")
	}
	if VersionScheme("2026.10.25") != "date" || VersionScheme("v1.2.3") != "semver" {
		t.Fatal("scheme classification broken")
	}
	if Reconcilable("nightly-20260101", "v1.2.3") {
		t.Error("nightly and semver should not be reconcilable")
	}
	if !Reconcilable("v1.2.3", "v1.10.0") {
		t.Error("semver pair should reconcile")
	}
	if Reconcilable("2.0.9.1", "2.0.9-1c19925b5") {
		t.Error("commit-hash tail must not reconcile with a clean version")
	}
	if !HasHashTail("2.0.9-1c19925b5") || HasHashTail("v1.2.3") {
		t.Error("hash tail detection broken")
	}
	dec, why := DecideUpdate(UpdateInput{
		InstalledTag: "2.0.9.1",
		CandidateTag: "2.0.9-1c19925b5",
	})
	if dec {
		t.Fatalf("hash-tail candidate must not count as an update: %s", why)
	}
}

func TestBelongsToApp(t *testing.T) {
	app := AppIdentity{
		AssetName: "auth-v4.4.25-windows-amd64.exe",
		Slug:      "auth",
	}
	if !BelongsToApp("auth-4.5.0-windows-amd64.exe", app) {
		t.Error("same stem not recognised")
	}
	if BelongsToApp("web-2.0-windows-amd64.exe", app) {
		t.Error("web build accepted for auth app")
	}
	app2 := AppIdentity{AssetGlob: "auth-*-windows-amd64.exe"}
	if !BelongsToApp("auth-9.1-windows-amd64.exe", app2) {
		t.Error("glob not honoured")
	}
	if !BelongsToApp("AuthSetup-1.0.exe", AppIdentity{Slug: "auth"}) {
		t.Error("slug heuristic not honoured")
	}
}

func TestSelectReleaseMonorepo(t *testing.T) {
	now := time.Now()
	assets := func(prefix string) []catalog.Asset {
		return []catalog.Asset{
			{ID: 1, Name: prefix + "-4.5.0-windows-amd64.exe", Size: 10},
			{ID: 2, Name: prefix + "-4.5.0-windows-arm64.exe", Size: 9},
			{ID: 3, Name: prefix + "-4.5.0-linux-amd64.tar.gz", Size: 8},
		}
	}
	releases := []catalog.Release{
		{ID: 10, TagName: "web-v2.0.0", PublishedAt: now, Assets: assets("web")},
		{ID: 11, TagName: "auth-v4.5.0", PublishedAt: now.Add(-time.Hour), Assets: assets("auth")},
	}
	sel := SelectRelease(releases, catalog.Target{OS: "windows", Arch: "amd64"},
		AppIdentity{AssetName: "auth-4.4.25-windows-amd64.exe", Slug: "auth", VersionTag: "auth-v4.4.25"},
		false, true, "")
	if sel.Asset == nil || sel.Release.TagName != "auth-v4.5.0" || sel.Asset.Name != "auth-4.5.0-windows-amd64.exe" {
		t.Fatalf("wrong selection: %+v", sel)
	}
	if sel.PickedIndex != 0 || sel.SiblingCount != 2 {
		t.Fatalf("index/siblings = %d/%d", sel.PickedIndex, sel.SiblingCount)
	}

	// Newest auth release has no matching family: fallback off must refuse.
	releases = []catalog.Release{
		{ID: 20, TagName: "auth-v5.0.0", PublishedAt: now, Assets: []catalog.Asset{
			{ID: 21, Name: "auth-5.0.0-windows-amd64.msi"},
		}},
		{ID: 22, TagName: "auth-v4.9.0", PublishedAt: now.Add(-time.Hour), Assets: []catalog.Asset{
			{ID: 23, Name: "auth-4.9.0-windows-amd64.exe"},
		}},
	}
	app := AppIdentity{AssetGlob: "auth-*-windows-amd64.exe", VersionTag: "auth-v4.8.0"}
	if sel := SelectRelease(releases, catalog.Target{OS: "windows", Arch: "amd64"}, app, false, false, ""); sel.Asset != nil {
		t.Fatalf("fallback off still selected %s", sel.Asset.Name)
	}
	if sel := SelectRelease(releases, catalog.Target{OS: "windows", Arch: "amd64"}, app, false, true, ""); sel.Asset == nil || sel.Release.TagName != "auth-v4.9.0" {
		t.Fatalf("fallback on failed: %+v", sel)
	}
}

func TestSelectReleasePrerelease(t *testing.T) {
	now := time.Now()
	releases := []catalog.Release{
		{ID: 1, TagName: "v2.0.0-rc1", PublishedAt: now, Assets: []catalog.Asset{{ID: 11, Name: "app-2.0.0-rc1-windows.exe"}}},
		{ID: 2, TagName: "v1.9.0", PublishedAt: now.Add(-time.Hour), Assets: []catalog.Asset{{ID: 12, Name: "app-1.9.0-windows.exe"}}},
	}
	app := AppIdentity{AssetGlob: "app-*-windows.exe", VersionTag: "v1.8.0"}
	if sel := SelectRelease(releases, catalog.Target{OS: "windows", Arch: "amd64"}, app, false, true, ""); sel.Release.TagName != "v1.9.0" {
		t.Fatalf("prerelease leaked: %+v", sel)
	}
	if sel := SelectRelease(releases, catalog.Target{OS: "windows", Arch: "amd64"}, app, true, true, ""); sel.Release.TagName != "v2.0.0-rc1" {
		t.Fatalf("prerelease opt-in failed: %+v", sel)
	}
}

func TestSelectReleaseFilter(t *testing.T) {
	now := time.Now()
	releases := []catalog.Release{{ID: 1, TagName: "v1.0.0", PublishedAt: now, Assets: []catalog.Asset{
		{ID: 1, Name: "app-1.0.0-windows-setup.exe"},
		{ID: 2, Name: "app-1.0.0-windows-portable.zip"},
	}}}
	if sel := SelectRelease(releases, catalog.Target{OS: "windows", Arch: "amd64"},
		AppIdentity{}, false, true, `portable`); sel.Asset == nil || sel.Asset.Name != "app-1.0.0-windows-portable.zip" {
		t.Fatalf("filter not applied: %+v", sel)
	}
}

func TestDecideUpdateGuards(t *testing.T) {
	now := time.Now()
	base := UpdateInput{
		InstalledTag: "auth-v1.0.0", InstalledAssetID: 1, InstalledDigest: "sha256:aa",
		InstalledSize: 100, InstalledPublishedAt: now.Add(-24 * time.Hour),
		CandidateTag: "web-v2.0.0", CandidateAssetID: 2, CandidateDigest: "sha256:bb",
		CandidateSize: 200, CandidatePublishedAt: now,
	}
	if update, reason := DecideUpdate(base); update {
		t.Fatalf("cross-line update allowed (%s)", reason)
	}
	base.CandidateTag = "auth-v1.1.0-rc1"
	if update, reason := DecideUpdate(base); update {
		t.Fatalf("prerelease leaked without opt-in (%s)", reason)
	}
	base.IncludePrerelease = true
	if update, _ := DecideUpdate(base); !update {
		t.Fatal("prerelease opt-in did not update")
	}
	base.CandidateTag = "auth-v1.0.0"
	base.CandidateAssetID = 9
	base.CandidateDigest = "sha256:cc"
	base.CandidatePublishedAt = now
	if update, reason := DecideUpdate(base); !update {
		t.Fatalf("same tag rebuilt with new size not detected (%s)", reason)
	}
}
