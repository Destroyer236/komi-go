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
