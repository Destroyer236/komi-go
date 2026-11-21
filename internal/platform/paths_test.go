package platform

import "testing"

func TestValidDownloadURL(t *testing.T) {
	cases := []struct {
		url  string
		ok   bool
		desc string
	}{
		{"https://github.com/o/r/releases/download/v1/app.exe", true, "github"},
		{"https://release-assets.githubusercontent.com/x/app.exe", true, "github asset"},
		{"https://codeberg.org/o/r/releases/download/v1/app.exe", true, "codeberg"},
		{"https://git.example.com/o/r/releases/download/v1/app.exe", true, "self-hosted forgejo"},
		{"http://github.com/o/r/app.exe", false, "http"},
		{"https://user:pass@github.com/o/r/app.exe", false, "userinfo"},
		{"https://localhost/app.exe", false, "localhost"},
		{"https://127.0.0.1/app.exe", false, "loopback"},
		{"https://192.168.1.8/app.exe", false, "private ip"},
		{"", false, "empty"},
	}
	for _, c := range cases {
		err := ValidDownloadURL(c.url)
		if (err == nil) != c.ok {
			t.Fatalf("%s: ValidDownloadURL(%q) err=%v", c.desc, c.url, err)
		}
	}
}

func TestClassifyDownloadURL(t *testing.T) {
	cases := []struct {
		url        string
		sourceHost string
		trusted    bool
		host       string
	}{
		{"https://github.com/o/r/releases/download/v1/app.exe", "github.com", true, "github.com"},
		{"https://release-assets.githubusercontent.com/x/app.exe", "github.com", true, "release-assets.githubusercontent.com"},
		{"https://git.example.com/o/r/releases/download/v1/app.exe", "git.example.com", true, "git.example.com"},
		{"https://cdn.unknown-vendor.com/app.exe", "github.com", false, "cdn.unknown-vendor.com"},
		{"https://cdn.unknown-vendor.com/app.exe", "", false, "cdn.unknown-vendor.com"},
		{"http://github.com/o/r/app.exe", "github.com", false, "github.com"},
		{"https://169.254.169.254/app.exe", "github.com", false, "169.254.169.254"},
	}
	for _, c := range cases {
		trusted, host, err := ClassifyDownloadURL(c.url, c.sourceHost)
		if c.url == "" || err != nil {
			if c.trusted {
				t.Fatalf("%s: unexpected error %v", c.url, err)
			}
			continue
		}
		if trusted != c.trusted || host != c.host {
			t.Fatalf("ClassifyDownloadURL(%q, %q) = (%v, %q), want (%v, %q)",
				c.url, c.sourceHost, trusted, host, c.trusted, c.host)
		}
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"../../evil.exe":  "evil.exe",
		`..\..\evil.exe`:  "evil.exe",
		`C:\temp\app.exe`: "app.exe",
		"NUL.exe":         "_NUL.exe",
		"  app?.zip ":     "app_.zip",
	}
	for in, want := range cases {
		if got := SanitizeFileName(in); got != want {
			t.Fatalf("SanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
