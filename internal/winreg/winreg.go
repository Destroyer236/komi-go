// Package winreg wraps the few registry operations the app needs: URL protocol
// registration, run-at-login and the Uninstall keys used to confirm installs,
// discover real install locations and enumerate externally installed apps.
package winreg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath    = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName  = "KomiGo"
	protoRoot     = `Software\Classes\komi`
	uninstallPath = `Software\Microsoft\Windows\CurrentVersion\Uninstall`
)

// UninstallEntry is one row from the Windows "Apps & features" list.
type UninstallEntry struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	Publisher       string `json:"publisher"`
	InstallLocation string `json:"installLocation"`
	Uninstall       string `json:"uninstallString"`
	QuietUninstall  string `json:"quietUninstallString"`
	DisplayIcon     string `json:"displayIcon"`
	ProductCode     string `json:"productCode"`
	EstimatedKB     int64  `json:"estimatedKb"`
	Hive            string `json:"hive"`
	Key             string `json:"key"`
}

// ListUninstall enumerates HKLM (64/32-bit views) and HKCU uninstall keys.
func ListUninstall() ([]UninstallEntry, error) {
	var out []UninstallEntry
	roots := []struct {
		root registry.Key
		path string
		hive string
	}{
		{registry.LOCAL_MACHINE, uninstallPath, "HKLM"},
		{registry.LOCAL_MACHINE, `Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, "HKLM32"},
		{registry.CURRENT_USER, uninstallPath, "HKCU"},
	}
	var firstErr error
	for _, r := range roots {
		k, err := registry.OpenKey(r.root, r.path, registry.READ)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		names, err := k.ReadSubKeyNames(-1)
		k.Close()
		if err != nil {
			continue
		}
		for _, name := range names {
			entry, err := readUninstall(r.root, r.path+`\`+name, name, r.hive)
			if err != nil || entry.Name == "" {
				continue
			}
			out = append(out, entry)
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func readUninstall(root registry.Key, path, keyName, hive string) (UninstallEntry, error) {
	k, err := registry.OpenKey(root, path, registry.READ)
	if err != nil {
		return UninstallEntry{}, err
	}
	defer k.Close()

	// System components and updates are not user-facing apps.
	if v, _, err := k.GetIntegerValue("SystemComponent"); err == nil && v == 1 {
		return UninstallEntry{}, errors.New("system component")
	}
	if v, _, err := k.GetIntegerValue("NoDisplay"); err == nil && v == 1 {
		return UninstallEntry{}, errors.New("hidden")
	}
	if _, _, err := k.GetStringValue("ParentKeyName"); err == nil {
		return UninstallEntry{}, errors.New("update package")
	}

	e := UninstallEntry{Key: keyName, Hive: hive}
	e.Name, _ = stringValue(k, "DisplayName")
	e.Version, _ = stringValue(k, "DisplayVersion")
	e.Publisher, _ = stringValue(k, "Publisher")
	e.InstallLocation, _ = stringValue(k, "InstallLocation")
	e.Uninstall, _ = stringValue(k, "UninstallString")
	e.QuietUninstall, _ = stringValue(k, "QuietUninstallString")
	e.DisplayIcon, _ = stringValue(k, "DisplayIcon")
	if v, _, err := k.GetIntegerValue("EstimatedSize"); err == nil {
		e.EstimatedKB = int64(v)
	}
	if strings.HasPrefix(keyName, "{") && strings.HasSuffix(keyName, "}") {
		e.ProductCode = keyName
	}
	if strings.HasPrefix(strings.ToLower(e.Name), "update for ") {
		return UninstallEntry{}, errors.New("update package")
	}
	return e, nil
}

func stringValue(k registry.Key, name string) (string, error) {
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

// FindByName returns the best uninstall entry whose display name matches.
func FindByName(name string) (UninstallEntry, bool) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return UninstallEntry{}, false
	}
	entries, err := ListUninstall()
	if err != nil {
		return UninstallEntry{}, false
	}
	best := -1
	for i, e := range entries {
		n := strings.ToLower(e.Name)
		if n == name {
			return e, true
		}
		if strings.Contains(n, name) || strings.Contains(name, n) {
			if best < 0 || len(e.Name) < len(entries[best].Name) {
				best = i
			}
		}
	}
	if best >= 0 {
		return entries[best], true
	}
	return UninstallEntry{}, false
}

// FindByProductCode locates an MSI product across HKLM/HKCU.
func FindByProductCode(code string) (UninstallEntry, bool) {
	code = strings.TrimSpace(code)
	if code == "" {
		return UninstallEntry{}, false
	}
	entries, err := ListUninstall()
	if err != nil {
		return UninstallEntry{}, false
	}
	for _, e := range entries {
		if strings.EqualFold(e.ProductCode, code) {
			return e, true
		}
	}
	return UninstallEntry{}, false
}

// FindByLocation matches an installation directory against InstallLocation.
func FindByLocation(dir string) (UninstallEntry, bool) {
	dir = strings.TrimSpace(strings.TrimRight(dir, `\/`))
	if dir == "" {
		return UninstallEntry{}, false
	}
	entries, err := ListUninstall()
	if err != nil {
		return UninstallEntry{}, false
	}
	for _, e := range entries {
		loc := strings.TrimSpace(strings.TrimRight(e.InstallLocation, `\/`))
		if loc != "" && strings.EqualFold(loc, dir) {
			return e, true
		}
	}
	return UninstallEntry{}, false
}

// EnsureURLScheme registers komi:// for the current user. Safe to call on every
// boot; it only rewrites when the executable moved.
func EnsureURLScheme(exePath string) error {
	if strings.TrimSpace(exePath) == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return err
		}
	}
	exePath, _ = filepath.Abs(exePath)
	command := fmt.Sprintf(`"%s" "%%1"`, exePath)

	k, _, err := registry.CreateKey(registry.CURRENT_USER, protoRoot, registry.SET_VALUE|registry.CREATE_SUB_KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	_ = k.SetStringValue("", "URL:Komi Go Protocol")
	_ = k.SetStringValue("URL Protocol", "")

	cmdKey, _, err := registry.CreateKey(registry.CURRENT_USER, protoRoot+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer cmdKey.Close()
	existing, _, _ := cmdKey.GetStringValue("")
	if existing == command {
		return nil
	}
	return cmdKey.SetStringValue("", command)
}

// URLSchemeRegistered reports whether komi:// is wired to this executable.
// An optional path overrides os.Executable (used by tests and portable runs).
func URLSchemeRegistered(exePath ...string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, protoRoot+`\shell\open\command`, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	if err != nil {
		return false
	}
	target := ""
	if len(exePath) > 0 {
		target = exePath[0]
	}
	if target == "" {
		target, _ = os.Executable()
	}
	if target == "" {
		return false
	}
	return strings.Contains(strings.ToLower(v), strings.ToLower(target))
}

// SetAutostart writes/removes the HKCU Run entry. startHidden adds --tray so a
// login start does not steal focus.
func SetAutostart(exePath string, enabled, startHidden bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		k, _, err = registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
		if err != nil {
			return err
		}
	}
	defer k.Close()
	if !enabled {
		err := k.DeleteValue(runValueName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	if exePath == "" {
		exePath, err = os.Executable()
		if err != nil {
			return err
		}
	}
	value := fmt.Sprintf(`"%s"`, exePath)
	if startHidden {
		value += " --tray"
	}
	return k.SetStringValue(runValueName, value)
}

// AutostartEnabled reports whether the Run value exists.
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValueName)
	return err == nil
}

// IsElevated reports whether the current process has an administrator token.
func IsElevated() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion`, registry.READ)
	if err != nil {
		return false
	}
	k.Close()
	return true
}
