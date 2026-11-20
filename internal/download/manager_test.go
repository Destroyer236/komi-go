package download

import (
	"strings"
	"testing"

	"komi-go/internal/catalog"
)

func TestSetMirrorsFiltersInvalid(t *testing.T) {
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	m.SetMirrors([]Mirror{
		{Name: "ok", Template: " https://ghproxy.example/https://github.com/{url} "},
		{Name: "empty", Template: "  "},
		{Name: "http", Template: "http://mirror.example/{url}"},
		{Name: "no-placeholder", Template: "https://mirror.example/fetch"},
		{Name: "", Template: "https://second.example/{url}"},
	})
	if len(m.mirrors) != 2 {
		t.Fatalf("mirrors = %#v", m.mirrors)
	}
	if !strings.Contains(m.mirrors[0].Template, "{url}") {
		t.Fatalf("first mirror = %#v", m.mirrors[0])
	}
}

func TestStartRejectsBadURL(t *testing.T) {
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	_, err := m.Start(Request{
		RepoKey: "o/r",
		Asset:   catalog.Asset{Name: "app.exe", DownloadURL: "http://example.com/app.exe"},
	})
	if err == nil {
		t.Fatal("expected http URL to be rejected")
	}
}

func TestPresetMirrorsAreValid(t *testing.T) {
	list := PresetMirrors()
	if len(list) < 4 {
		t.Fatalf("presets = %d, want >= 4", len(list))
	}
	for _, m := range list {
		if !strings.HasPrefix(m.Template, "https://") || !strings.Contains(m.Template, "{url}") {
			t.Fatalf("bad preset %#v", m)
		}
		if strings.TrimSpace(m.Name) == "" {
			t.Fatalf("unnamed preset %#v", m)
		}
	}
}

func TestTestMirrorsRejectsInvalidURL(t *testing.T) {
	m := New(t.TempDir(), nil, func() string { return "" }, nil)
	r := m.TestMirrors(t.Context(), []Mirror{{Name: "bad", Template: "http://x/{url}"}}, "https://example.com/a.zip")
	if len(r) != 1 || r[0].OK || r[0].Latency != -1 {
		t.Fatalf("result = %#v", r)
	}
}
