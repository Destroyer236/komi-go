package installer

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitUninstallStringQuoted(t *testing.T) {
	exe, args, err := splitUninstallString(`"C:\Program Files\Komi\uninstall.exe" /S /quiet`)
	if err != nil {
		t.Fatal(err)
	}
	if exe != `C:\Program Files\Komi\uninstall.exe` {
		t.Fatalf("exe = %q", exe)
	}
	if len(args) != 2 || args[0] != "/S" || args[1] != "/quiet" {
		t.Fatalf("args = %#v", args)
	}
}

func TestSplitUninstallStringUnquoted(t *testing.T) {
	exe, args, err := splitUninstallString(`C:\Tools\App\uninstall.exe /S`)
	if err != nil {
		t.Fatal(err)
	}
	if exe != `C:\Tools\App\uninstall.exe` {
		t.Fatalf("exe = %q", exe)
	}
	if len(args) != 1 || args[0] != "/S" {
		t.Fatalf("args = %#v", args)
	}
}

func TestSplitUninstallStringMSI(t *testing.T) {
	exe, args, err := splitUninstallString(`msiexec.exe /x {1234-ABCD}`)
	if err != nil {
		t.Fatal(err)
	}
	if exe != "msiexec.exe" {
		t.Fatalf("exe = %q", exe)
	}
	if len(args) != 2 || args[0] != "/x" {
		t.Fatalf("args = %#v", args)
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	dest := t.TempDir()
	for _, name := range []string{`../evil.exe`, `..\evil.exe`, `a/../../evil.exe`, `C:\Windows\evil.exe`} {
		if _, err := safeJoin(dest, name); err == nil {
			t.Fatalf("safeJoin(%q) allowed traversal", name)
		}
	}
	if _, err := safeJoin(dest, `bin/app.exe`); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "app.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"app.exe":       "MZ fake",
		"../escape.txt": "escaped",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if _, _, err := ExtractArchive(zipPath, dest, 1<<20); err == nil {
		t.Fatal("expected traversal archive to be rejected")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("path traversal wrote outside dest: %v", err)
	}
}

func TestExtractZipFindsEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "app.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"README.txt":        "readme",
		"bin/app.exe":       "MZ fake",
		"bin/sub/other.exe": "MZ fake",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	entry, _, err := ExtractArchive(zipPath, dest, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.ToLower(entry), `bin\app.exe`) && !strings.HasSuffix(strings.ToLower(entry), "bin/app.exe") {
		t.Fatalf("entry = %q", entry)
	}
}
