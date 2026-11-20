package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExtractArchive unpacks zip, tar.gz/tgz and 7z archives. It guards against
// path traversal and zip bombs, and returns the shallowest .exe entry point.
func ExtractArchive(archivePath, dest string, maxBytes int64) (string, int64, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", 0, err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return "", 0, err
	}
	low := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(low, ".zip"):
		written, err := extractZip(archivePath, absDest, maxBytes)
		if err != nil {
			return "", written, err
		}
		entry, err := findEntryExe(absDest)
		return entry, written, err
	case strings.HasSuffix(low, ".tar.gz"), strings.HasSuffix(low, ".tgz"):
		written, err := extractTarGz(archivePath, absDest, maxBytes)
		if err != nil {
			return "", written, err
		}
		entry, err := findEntryExe(absDest)
		return entry, written, err
	case strings.HasSuffix(low, ".tar"):
		written, err := extractTarFile(archivePath, absDest, maxBytes)
		if err != nil {
			return "", written, err
		}
		entry, err := findEntryExe(absDest)
		return entry, written, err
	case strings.HasSuffix(low, ".7z"):
		if err := extract7z(archivePath, absDest); err != nil {
			return "", 0, err
		}
		entry, err := findEntryExe(absDest)
		if err != nil {
			return "", 0, err
		}
		return entry, DirSize(absDest, 4<<30), nil
	default:
		return "", 0, fmt.Errorf("不支持的压缩格式：%s", filepath.Ext(archivePath))
	}
}

func extractZip(zipPath, absDest string, maxBytes int64) (int64, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return 0, fmt.Errorf("无法读取压缩包：%w", err)
	}
	defer r.Close()

	var written int64
	for _, f := range r.File {
		target, err := safeJoin(absDest, f.Name)
		if err != nil {
			return written, err
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if f.UncompressedSize64 > 1<<31 {
			return written, errors.New("压缩包条目过大，已拒绝解压")
		}
		written += int64(f.UncompressedSize64)
		if maxBytes > 0 && written > maxBytes {
			return written, errors.New("压缩包超过大小上限，已中止")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return written, err
		}
		if err := copyZipFile(f, target); err != nil {
			return written, err
		}
	}
	return written, nil
}

func copyZipFile(f *zip.File, target string) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, io.LimitReader(src, 1<<31))
	return err
}

func extractTarGz(path, absDest string, maxBytes int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("无法读取 gzip：%w", err)
	}
	defer gz.Close()
	return extractTar(gz, absDest, maxBytes)
}

func extractTarFile(path, absDest string, maxBytes int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return extractTar(f, absDest, maxBytes)
}

func extractTar(r io.Reader, absDest string, maxBytes int64) (int64, error) {
	tr := tar.NewReader(r)

	var written int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, err
		}
		target, err := safeJoin(absDest, hdr.Name)
		if err != nil {
			return written, err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			if hdr.Size > 1<<31 {
				return written, errors.New("压缩包条目过大，已拒绝解压")
			}
			written += hdr.Size
			if maxBytes > 0 && written > maxBytes {
				return written, errors.New("压缩包超过大小上限，已中止")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return written, err
			}
			dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return written, err
			}
			_, copyErr := io.Copy(dst, io.LimitReader(tr, hdr.Size))
			dst.Close()
			if copyErr != nil {
				return written, copyErr
			}
		default:
			// Symlinks and devices are skipped: a portable app does not need them
			// and they are a common escape primitive.
			continue
		}
	}
	return written, nil
}

// extract7z uses the libarchive-based tar.exe shipped with Windows 10+.
func extract7z(path, absDest string) error {
	tarExe, err := exec.LookPath("tar.exe")
	if err != nil {
		candidate := filepath.Join(os.Getenv("SystemRoot"), "System32", "tar.exe")
		if _, statErr := os.Stat(candidate); statErr == nil {
			tarExe = candidate
		} else {
			return errors.New("系统没有 tar.exe，无法解压 7z；请改下载 zip 版本")
		}
	}
	listOut, err := exec.Command(tarExe, "-tf", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("7z 列表失败：%s", tail(string(listOut), 160))
	}
	for _, line := range strings.Split(string(listOut), "\n") {
		name := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if name == "" {
			continue
		}
		if _, err := safeJoin(absDest, name); err != nil {
			return err
		}
	}
	cmd := exec.Command(tarExe, "-xf", path, "-C", absDest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("7z 解压失败：%s", tail(string(out), 200))
	}
	return nil
}

// safeJoin rejects absolute paths, drive letters and any ".." traversal.
func safeJoin(absDest, name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.Contains(clean, ":") {
		return "", fmt.Errorf("压缩包条目使用绝对路径：%s", name)
	}
	target := filepath.Join(absDest, clean)
	if target != absDest && !strings.HasPrefix(target, absDest+string(os.PathSeparator)) {
		return "", fmt.Errorf("压缩包条目越界：%s", name)
	}
	return target, nil
}

// findEntryExe picks the shallowest .exe, preferring a top-level launcher.
func findEntryExe(dir string) (string, error) {
	best := ""
	bestDepth := 1 << 30
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".exe") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		depth := strings.Count(rel, string(os.PathSeparator))
		if depth < bestDepth {
			best, bestDepth = path, depth
		}
		return nil
	})
	if best == "" {
		return "", errors.New("压缩包里没有找到 .exe")
	}
	return best, nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
