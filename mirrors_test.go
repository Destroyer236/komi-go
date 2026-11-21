package main

import (
	"encoding/json"
	"testing"

	"komi-go/internal/download"
)

// TestIsLegacyPresetListOnlyMatchesUntouchedLists guards the migration: only a
// stored list that is exactly what an older build shipped may be replaced, so a
// hand-edited list is never overwritten.
func TestIsLegacyPresetListOnlyMatchesUntouchedLists(t *testing.T) {
	legacy, err := json.Marshal(download.LegacyPresets())
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	if !isLegacyPresetList(string(legacy)) {
		t.Fatal("shipped legacy list should be recognised")
	}

	// The same templates, but reordered by hand: still the shipped set.
	var list []download.Mirror
	if err := json.Unmarshal(legacy, &list); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	list = append(list[1:], list[0])
	reordered, _ := json.Marshal(list)
	if !isLegacyPresetList(string(reordered)) {
		t.Fatal("reordered legacy list should be recognised")
	}

	custom := append([]download.Mirror(nil), list[:len(list)-1]...)
	custom = append(custom, download.Mirror{Name: "Mine", Template: "https://mirror.example.com/{url}"})
	raw, _ := json.Marshal(custom)
	if isLegacyPresetList(string(raw)) {
		t.Fatal("a list containing a user entry must not be replaced")
	}

	for _, bad := range []string{"", "null", "{}", "[]", "not json"} {
		if isLegacyPresetList(bad) {
			t.Fatalf("%q should not look like the legacy list", bad)
		}
	}
}

// TestPresetMirrorsHaveNoDuplicates keeps the racing list honest: a duplicated
// template would probe the same host twice and show up twice in Settings.
func TestPresetMirrorsHaveNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range download.PresetMirrors() {
		if seen[m.Template] {
			t.Fatalf("duplicate preset %q", m.Template)
		}
		seen[m.Template] = true
	}
}

// TestCurrentPresetsDifferFromLegacy documents that the upgrade actually brings
// something new, which is the point of the migration.
func TestCurrentPresetsDifferFromLegacy(t *testing.T) {
	current := download.PresetMirrors()
	legacy := download.LegacyPresets()
	known := map[string]bool{}
	for _, m := range legacy {
		known[m.Template] = true
	}
	added := 0
	for _, m := range current {
		if !known[m.Template] {
			added++
		}
	}
	if added == 0 {
		t.Fatal("presets gained no new mirror; the migration would be pointless")
	}
}
