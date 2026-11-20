package platform

import (
	"errors"
	"net/url"
	"os/exec"
	"strings"
)

// OpenURL opens an http(s) URL in the default browser. Anything else (file:,
// javascript:, shell handlers) is refused so a malicious release note cannot
// turn into local code execution.
func OpenURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("链接为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("链接格式不正确")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("只允许打开 http/https 链接")
	}
	if u.Hostname() == "" {
		return errors.New("链接缺少主机名")
	}
	if strings.ContainsAny(raw, "\r\n\"'") {
		return errors.New("链接包含非法字符")
	}
	// rundll32 resolves the protocol handler without going through cmd.exe,
	// which avoids shell metacharacter injection.
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", raw).Start()
}
