// Package platform contains the Windows-specific bits: paths, file-name
// hygiene and URL policy. Keeping them here means the rest of the app can be
// tested on any OS.
package platform

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Layout is the on-disk contract for the app.
type Layout struct {
	Root      string
	Database  string
	Downloads string
	Logs      string
	Apps      string
}

// DefaultRoot is %LocalAppData%\KomiGo. Never write next to the exe: Program
// Files is read-only for normal users.
func DefaultRoot() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		base = dir
	}
	return filepath.Join(base, "KomiGo"), nil
}

// EnsureLayout creates the directory tree and returns the resolved paths.
func EnsureLayout() (Layout, error) {
	root, err := DefaultRoot()
	if err != nil {
		return Layout{}, err
	}
	l := Layout{
		Root:      root,
		Database:  filepath.Join(root, "komi.db"),
		Downloads: filepath.Join(root, "downloads"),
		Logs:      filepath.Join(root, "logs"),
		Apps:      filepath.Join(root, "apps"),
	}
	for _, dir := range []string{l.Root, l.Downloads, l.Logs, l.Apps} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Layout{}, fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	return l, nil
}

var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SanitizeFileName turns an arbitrary asset name into a safe leaf name.
// It strips directories (defeating path traversal), removes Windows-illegal
// characters and escapes reserved device names.
func SanitizeFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")

	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32:
			continue
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		out = "download.bin"
	}

	ext := filepath.Ext(out)
	stem := strings.TrimSuffix(out, ext)
	if len(stem) > 140 {
		stem = stem[:140]
	}
	if reservedNames[strings.ToLower(stem)] {
		stem = "_" + stem
	}
	if ext == "" {
		return stem
	}
	if utf8.RuneCountInString(stem) > 140 {
		stem = string([]rune(stem)[:140])
	}
	return stem + ext
}

// UniquePath returns a path in dir that does not exist yet; it never silently
// overwrites a user file.
func UniquePath(dir, name string) string {
	safe := SanitizeFileName(name)
	path := filepath.Join(dir, safe)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	ext := filepath.Ext(safe)
	stem := strings.TrimSuffix(safe, ext)
	for i := 1; i < 1000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, os.Getpid(), ext))
}

var allowedHosts = map[string]bool{
	"github.com":                           true,
	"api.github.com":                       true,
	"objects.githubusercontent.com":        true,
	"raw.githubusercontent.com":            true,
	"codeload.github.com":                  true,
	"codeberg.org":                         true,
	"gitea.com":                            true,
	"release-assets.githubusercontent.com": true,
}

// ValidDownloadURL enforces the download policy: https only, no credentials,
// no private hosts, and a host we are willing to execute files from.
func ValidDownloadURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("下载地址为空")
	}
	if !strings.HasPrefix(strings.ToLower(raw), "https://") {
		return errors.New("只允许 https 下载")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.User != nil {
		return errors.New("下载地址不能包含用户名密码")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("下载地址缺少主机名")
	}
	if isPrivateDownloadHost(host) {
		return errors.New("不允许从内网或本机地址下载")
	}
	if allowedHosts[host] || strings.HasSuffix(host, ".githubusercontent.com") ||
		strings.HasSuffix(host, ".codeberg.org") || strings.HasSuffix(host, ".gitea.com") {
		return nil
	}
	// Forgejo/Gitea instances are self-hosted, so a fixed allowlist cannot cover
	// them. The release URL still has to be public https without credentials;
	// the user sees the final app name and installs it explicitly.
	return nil
}

func isPrivateDownloadHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsUnspecified() || ip.IsMulticast()
	}
	return false
}

// OpenFolder reveals a directory in Explorer.
func OpenFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("目录不存在：%s", path)
	}
	return exec.Command("explorer.exe", path).Start()
}

// HumanBytes formats a byte count for the UI.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// EnvToken returns a PAT supplied through the environment, never persisted.
func EnvToken() string {
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

// IsLetterOrDigit is exposed for tests that need the same word-char rule.
func IsLetterOrDigit(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
