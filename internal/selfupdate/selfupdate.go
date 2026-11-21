// Package selfupdate implements "Komi Go updates itself" without an external
// updater framework: check the feed repo, download the installer, then hand off
// to a small batch script that waits for the process to exit before running it.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"komi-go/internal/catalog"
	"komi-go/internal/github"
	"komi-go/internal/matcher"
)

// Info is what the settings page needs to show.
type Info struct {
	Available   bool      `json:"available"`
	Current     string    `json:"current"`
	Latest      string    `json:"latest"`
	Tag         string    `json:"tag"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	Digest      string    `json:"digest"`
	Notes       string    `json:"notes"`
	PublishedAt time.Time `json:"publishedAt"`
}

// Check looks at the newest stable release of feedRepo and picks a Windows
// installer asset.
func Check(ctx context.Context, c *github.Client, feedRepo, current string) (Info, error) {
	ref, err := github.ParseRef(feedRepo)
	if err != nil {
		return Info{}, errors.New("自更新仓库格式应为 owner/repo")
	}
	if ref.Host != "github.com" {
		return Info{}, errors.New("自更新目前只支持 GitHub 仓库")
	}
	releases, _, err := c.Releases(ctx, ref.Owner, ref.Name, 10, false)
	if err != nil {
		return Info{}, err
	}
	for _, rel := range releases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		asset := pickInstaller(rel.Assets, runtime.GOARCH)
		if asset == nil {
			continue
		}
		info := Info{
			Current: current, Latest: rel.TagName, Tag: rel.TagName,
			Name: asset.Name, URL: asset.DownloadURL, Size: asset.Size,
			Digest: asset.Digest,
			Notes:  rel.Body, PublishedAt: rel.PublishedAt,
		}
		info.Available = matcher.CompareVersion(rel.TagName, current) > 0
		return info, nil
	}
	return Info{Current: current}, nil
}

func pickInstaller(assets []catalog.Asset, arch string) *catalog.Asset {
	var msi, exe, zip *catalog.Asset
	for i := range assets {
		a := &assets[i]
		if a.Size <= 0 || a.DownloadURL == "" {
			continue
		}
		low := strings.ToLower(a.Name)
		if strings.Contains(low, "sha256") || strings.Contains(low, "checksum") {
			continue
		}
		isWin := strings.Contains(low, "windows") || strings.Contains(low, "win64") ||
			strings.Contains(low, "win-") || strings.Contains(low, "amd64") ||
			strings.Contains(low, "x64") || strings.Contains(low, "arm64")
		if !isWin || !assetArchMatches(low, arch) {
			continue
		}
		switch {
		case strings.HasSuffix(low, ".msi"):
			if msi == nil {
				msi = a
			}
		case strings.HasSuffix(low, ".exe"):
			if exe == nil {
				exe = a
			}
		case strings.HasSuffix(low, ".zip"):
			if zip == nil {
				zip = a
			}
		}
	}
	if msi != nil {
		return msi
	}
	if exe != nil {
		return exe
	}
	return zip
}

// assetArchMatches rejects an asset that explicitly names the other CPU
// architecture, while still accepting a generic "windows" asset.
func assetArchMatches(low, arch string) bool {
	switch {
	case strings.Contains(low, "arm64") || strings.Contains(low, "aarch64"):
		return arch == "arm64"
	case strings.Contains(low, "amd64") || strings.Contains(low, "x86_64"):
		return arch == "amd64"
	}
	return true
}

// Download streams the installer with progress reporting.
func Download(ctx context.Context, hc *http.Client, info Info, destDir string, onProgress func(done, total int64)) (string, error) {
	if info.URL == "" {
		return "", errors.New("没有可下载的自更新资产")
	}
	// Self-update executes whatever it downloads, so an unverifiable package is
	// refused rather than installed on trust.
	if info.Digest == "" {
		return "", errors.New("Release 未提供 digest，已拒绝执行无法校验的自更新；请手动下载安装包")
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	name := info.Name
	if name == "" {
		name = "komi-go-update.exe"
	}
	path := filepath.Join(destDir, name)
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	total := info.Size
	if total <= 0 {
		total = resp.ContentLength
	}
	var done int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				os.Remove(tmp)
				return "", err
			}
			done += int64(n)
			if onProgress != nil {
				onProgress(done, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			os.Remove(tmp)
			return "", readErr
		}
		if ctx.Err() != nil {
			f.Close()
			os.Remove(tmp)
			return "", ctx.Err()
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if info.Digest != "" {
		if err := verifyFileDigest(tmp, info.Digest); err != nil {
			os.Remove(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// verifyFileDigest compares the SHA-256 of path with a GitHub
// "sha256:<hex>" digest. An unverifiable digest is treated as a failure: a
// self-update that installs bytes we cannot check is exactly the hole a
// compromised feed repo would use.
func verifyFileDigest(path, digest string) error {
	sum, err := hashFile(path)
	if err != nil {
		return err
	}
	want := digest
	if i := strings.Index(want, ":"); i > 0 {
		want = want[i+1:]
	}
	if !strings.EqualFold(sum, want) {
		return fmt.Errorf("自更新包校验失败：期望 %s，实际 %s", want, sum)
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Launch writes a detached updater script and starts it. The caller should
// quit immediately afterwards so the installer can replace the binary.
func Launch(installerPath, appExe string) error {
	// A predictable script path in %TEMP% is writable by any local process; use
	// a random name so the updater cannot be pre-planted or swapped.
	f, err := os.CreateTemp("", "komi-self-update-*.cmd")
	if err != nil {
		return err
	}
	script := f.Name()
	var run string
	switch strings.ToLower(filepath.Ext(installerPath)) {
	case ".msi":
		run = fmt.Sprintf(`msiexec.exe /i "%s" /passive /norestart`, installerPath)
	case ".exe":
		run = fmt.Sprintf(`"%s"`, installerPath)
	default:
		run = fmt.Sprintf(`explorer.exe "%s"`, installerPath)
	}
	content := "@echo off\r\n" +
		"timeout /t 2 /nobreak >nul\r\n" +
		run + "\r\n" +
		"timeout /t 2 /nobreak >nul\r\n" +
		fmt.Sprintf("if exist \"%s\" start \"\" \"%s\"\r\n", appExe, appExe) +
		"del \"%~f0\"\r\n"
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(script)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(script)
		return err
	}
	cmd := exec.Command("cmd.exe", "/c", "start", "", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008}
	return cmd.Start()
}
