package github

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestBackendLiveSmoke is opt-in because it depends on the public network.
// Run with: $env:KOMI_LIVE='1'; go test ./internal/github -run TestBackendLiveSmoke -v
func TestBackendLiveSmoke(t *testing.T) {
	if os.Getenv("KOMI_LIVE") != "1" {
		t.Skip("set KOMI_LIVE=1 to run the public backend smoke test")
	}
	b := NewBackend(nil)
	ctx := context.Background()

	list, _, err := b.Discover(ctx, "trending", 1, true)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("discover returned no repos")
	}
	for _, r := range list {
		if !r.PlatformReady {
			t.Fatalf("onlyInstallable leaked %s", r.FullName)
		}
	}
	t.Logf("discover: %d items, first=%s tag=%s", len(list), list[0].FullName, list[0].LatestReleaseTag)

	info, _, err := b.Repo(ctx, "neovim", "neovim")
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	if info.FullName == "" || info.Stars == 0 {
		t.Fatalf("repo enrichment empty: %+v", info)
	}

	rels, _, err := b.Releases(ctx, "neovim", "neovim", 10, false)
	if err != nil {
		t.Fatalf("releases: %v", err)
	}
	if len(rels) == 0 || len(rels[0].Assets) == 0 {
		t.Fatalf("releases empty: %+v", rels)
	}

	readme, _, err := b.Readme(ctx, "neovim", "neovim")
	if err != nil {
		t.Fatalf("readme: %v", err)
	}
	if !strings.Contains(readme, "Neovim") {
		t.Fatalf("readme decoding looks wrong: %.80q", readme)
	}
	t.Logf("releases=%d readme=%d bytes", len(rels), len(readme))
}
