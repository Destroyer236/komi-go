// Package installer hands downloaded artifacts to the Windows installer stack
// and then confirms the result in the registry. The app itself never runs
// elevated: UAC is requested per-install by the installer we launch.
package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"komi-go/internal/winreg"
)

// Result describes what happened after handing off to the installer.
type Result struct {
	Pending     bool   `json:"pending"`
	NeedReboot  bool   `json:"needReboot"`
	Unverified  bool   `json:"unverified"`
	Message     string `json:"message"`
	ExitCode    int    `json:"exitCode"`
	ProductCode string `json:"productCode"`
	InstallDir  string `json:"installDir"`
	Entry       string `json:"entry"`
	Size        int64  `json:"size"`
}

var (
	shell32           = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

const (
	swShowNormal           = 1
	errorElevationRequired = 740
	msiNotInstalled        = 1605
)

// Install dispatches on extension. MSI runs with /passive and is waited on;
// EXE installers are handed to the shell and confirmed through the registry by
// the caller (pending state).
func Install(ctx context.Context, path string) (Result, error) {
	if _, err := os.Stat(path); err != nil {
		return Result{}, fmt.Errorf("安装包不存在：%s", path)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".msi":
		return installMSI(ctx, path)
	case ".msix", ".appx":
		return Result{}, errors.New("暂不直接安装 MSIX，请解压后使用系统安装器")
	case ".exe":
		return launchExe(path)
	default:
		return Result{}, fmt.Errorf("不支持直接安装 %s", filepath.Ext(path))
	}
}

func installMSI(ctx context.Context, path string) (Result, error) {
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("komi-msi-%d.log", time.Now().UnixNano()))
	cmd := exec.CommandContext(ctx, "msiexec.exe", "/i", path, "/passive", "/norestart", "/L*v", logPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	err := cmd.Run()
	result := Result{InstallDir: filepath.Dir(path)}
	if err == nil {
		result.ExitCode = 0
		result.Message = "安装完成"
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		switch result.ExitCode {
		case 0:
			result.Message = "安装完成"
			return result, nil
		case 3010:
			result.NeedReboot = true
			result.Message = "安装完成，需要重启"
			return result, nil
		case 1223:
			return Result{}, errors.New("已取消安装（UAC 被拒绝）")
		case msiNotInstalled:
			return Result{}, errors.New("安装器报告产品未安装")
		case 1603, 1925:
			// Per-machine MSIs cannot install from an unelevated process; retry
			// through UAC instead of surfacing a bare msiexec exit code.
			return relaunchMSIElevated(path, logPath)
		default:
			return Result{}, fmt.Errorf("msiexec 退出码 %d（日志：%s）", result.ExitCode, logPath)
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return Result{}, errors.New("安装超时，安装器可能仍在运行")
	}
	return Result{}, err
}

func relaunchMSIElevated(path, logPath string) (Result, error) {
	params := fmt.Sprintf(`/i "%s" /passive /norestart /L*v "%s"`, path, logPath)
	code, err := shellExecute("runas", "msiexec.exe", params, filepath.Dir(path), swShowNormal)
	if err == nil {
		return Result{Pending: true, Message: "需要管理员权限，已弹出 UAC 确认；安装完成后会在应用库里自动确认"}, nil
	}
	if code == errorElevationRequired {
		return Result{}, errors.New("安装器需要管理员权限，但提权被拒绝")
	}
	return Result{}, fmt.Errorf("无法以管理员权限启动安装器：%v", err)
}

func launchExe(path string) (Result, error) {
	code, err := shellExecute("open", path, "", filepath.Dir(path), swShowNormal)
	if err == nil {
		return Result{Pending: true, Message: "安装器已启动，正在等待系统确认"}, nil
	}
	if code == errorElevationRequired {
		if _, err2 := shellExecute("runas", path, "", filepath.Dir(path), swShowNormal); err2 != nil {
			return Result{}, errors.New("安装器需要管理员权限，但提权被拒绝")
		}
		return Result{Pending: true, Message: "已请求管理员权限，正在等待系统确认"}, nil
	}
	return Result{}, fmt.Errorf("无法启动安装器：%v", err)
}

func shellExecute(verb, file, params, dir string, show int) (int, error) {
	verbPtr, _ := syscall.UTF16PtrFromString(verb)
	filePtr, _ := syscall.UTF16PtrFromString(file)
	var paramsPtr, dirPtr *uint16
	if params != "" {
		paramsPtr, _ = syscall.UTF16PtrFromString(params)
	}
	if dir != "" {
		dirPtr, _ = syscall.UTF16PtrFromString(dir)
	}
	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verbPtr)),
		uintptr(unsafe.Pointer(filePtr)),
		uintptr(unsafe.Pointer(paramsPtr)),
		uintptr(unsafe.Pointer(dirPtr)),
		uintptr(show),
	)
	if ret <= 32 {
		return int(ret), fmt.Errorf("ShellExecute 返回码 %d", ret)
	}
	return int(ret), nil
}

// ConfirmInstall polls the Uninstall registry until the product shows up. It
// returns a zero result and false when the timeout expires.
func ConfirmInstall(ctx context.Context, displayName, installDir string, timeout time.Duration) (UninstallEntry, bool) {
	return ConfirmAny(ctx, []string{displayName}, installDir, timeout)
}

// ConfirmAny tries several candidate display names (repo name, asset stem,
// installer name) because vendors rarely use the GitHub repo name verbatim.
func ConfirmAny(ctx context.Context, names []string, installDir string, timeout time.Duration) (UninstallEntry, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if e, ok := lookupInstallAny(names, installDir); ok {
			return e, true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return UninstallEntry{}, false
		}
		select {
		case <-ctx.Done():
			return UninstallEntry{}, false
		case <-time.After(2 * time.Second):
		}
	}
}

func lookupInstallAny(names []string, installDir string) (UninstallEntry, bool) {
	if installDir != "" {
		if e, ok := winreg.FindByLocation(installDir); ok {
			return e, true
		}
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if e, ok := winreg.FindByName(name); ok {
			return e, true
		}
	}
	return UninstallEntry{}, false
}

// UninstallEntry is re-exported so app.go does not need the registry package.
type UninstallEntry = winreg.UninstallEntry

func lookupInstall(displayName, installDir string) (UninstallEntry, bool) {
	if installDir != "" {
		if e, ok := winreg.FindByLocation(installDir); ok {
			return e, true
		}
	}
	if displayName != "" {
		if e, ok := winreg.FindByName(displayName); ok {
			return e, true
		}
	}
	return UninstallEntry{}, false
}

// Uninstall removes an app via MSI product code (preferred) or its
// UninstallString. Exit code 1605 means it was already gone.
func Uninstall(ctx context.Context, productCode, uninstallString string) (Result, error) {
	productCode = strings.TrimSpace(productCode)
	if strings.HasPrefix(productCode, "{") && strings.HasSuffix(productCode, "}") {
		cmd := exec.CommandContext(ctx, "msiexec.exe", "/x", productCode, "/passive", "/norestart")
		err := cmd.Run()
		if err == nil {
			return Result{Message: "已卸载"}, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 0:
				return Result{Message: "已卸载"}, nil
			case 3010:
				return Result{NeedReboot: true, Message: "已卸载，需要重启"}, nil
			case msiNotInstalled, 1606:
				return Result{Message: "系统里已经找不到这个产品"}, nil
			case 1223:
				return Result{}, errors.New("已取消卸载（UAC 被拒绝）")
			default:
				return Result{}, fmt.Errorf("msiexec 卸载退出码 %d", exitErr.ExitCode())
			}
		}
		return Result{}, err
	}

	exe, args, err := splitUninstallString(uninstallString)
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 0, 3010:
				return Result{Message: "卸载程序已结束"}, nil
			case 1605:
				return Result{Message: "系统里已经找不到这个产品"}, nil
			default:
				return Result{}, fmt.Errorf("卸载程序退出码 %d，可能需要管理员权限", exitErr.ExitCode())
			}
		}
		return Result{}, fmt.Errorf("无法运行卸载程序：%w", err)
	}
	return Result{Message: "卸载程序已结束"}, nil
}

func splitUninstallString(raw string) (string, []string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil, errors.New("系统没有登记卸载命令，请到“应用和功能”里卸载")
	}
	var exe string
	if strings.HasPrefix(s, `"`) {
		if end := strings.Index(s[1:], `"`); end >= 0 {
			q := end + 1
			exe = s[1:q]
			s = strings.TrimSpace(s[q+1:])
		}
	} else {
		// Unquoted paths with spaces are ambiguous; split at the first .exe.
		if i := strings.Index(strings.ToLower(s), ".exe"); i >= 0 {
			exe = s[:i+4]
			s = strings.TrimSpace(s[i+4:])
		} else {
			exe = s
			s = ""
		}
	}
	if exe == "" {
		return "", nil, errors.New("卸载命令格式异常")
	}
	return exe, strings.Fields(s), nil
}

// DirSize walks a directory with a hard cap so a huge install cannot stall UI.
func DirSize(dir string, limit int64) int64 {
	if strings.TrimSpace(dir) == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		if limit > 0 && total > limit {
			return filepath.SkipAll
		}
		return nil
	})
	return total
}

// GuessInstallDir resolves the directory a Windows installer most likely used.
func GuessInstallDir(productDir, exePath string) string {
	if productDir != "" {
		return productDir
	}
	if exePath != "" {
		return filepath.Dir(exePath)
	}
	return ""
}
