package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"komi-go/internal/catalog"
)

func TestPickInstallerChoosesArch(t *testing.T) {
	assets := []catalog.Asset{
		{Name: "KomiGo-0.3.0-amd64-setup.exe", Size: 10, DownloadURL: "https://x/a.exe"},
		{Name: "KomiGo-0.3.0-arm64-setup.exe", Size: 10, DownloadURL: "https://x/b.exe"},
		{Name: "KomiGo-0.3.0-amd64.sha256", Size: 10, DownloadURL: "https://x/c"},
	}
	amd := pickInstaller(assets, "amd64")
	if amd == nil || amd.Name != "KomiGo-0.3.0-amd64-setup.exe" {
		t.Fatalf("amd64 picked %#v", amd)
	}
	arm := pickInstaller(assets, "arm64")
	if arm == nil || arm.Name != "KomiGo-0.3.0-arm64-setup.exe" {
		t.Fatalf("arm64 picked %#v", arm)
	}
}

func TestPickInstallerPrefersMSIAndSkipsChecksum(t *testing.T) {
	assets := []catalog.Asset{
		{Name: "app-windows-amd64-setup.exe", Size: 10, DownloadURL: "https://x/a.exe"},
		{Name: "app-windows-amd64.msi", Size: 10, DownloadURL: "https://x/a.msi"},
		{Name: "app-windows-amd64-checksum.txt", Size: 10, DownloadURL: "https://x/a.txt"},
	}
	got := pickInstaller(assets, "amd64")
	if got == nil || got.Name != "app-windows-amd64.msi" {
		t.Fatalf("picked %#v", got)
	}
}

func TestVerifyFileDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.bin")
	body := []byte("komi go update payload")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	good := "sha256:" + hex.EncodeToString(sum[:])
	if err := verifyFileDigest(path, good); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	if err := verifyFileDigest(path, "sha256:"+hex.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("mismatched digest accepted")
	}
}

func TestDownloadRefusesMissingDigest(t *testing.T) {
	_, err := Download(t.Context(), nil, Info{URL: "https://example.com/a.exe"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("download without digest should be refused")
	}
}
