package selfupdate

import (
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
