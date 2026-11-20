package main

import (
	"testing"

	"komi-go/internal/catalog"
)

func TestSearchScoreIsCaseInsensitiveAndFuzzy(t *testing.T) {
	daozai := catalog.RepoInfo{
		FullName:    "Renly1994/xiaotang-daozai",
		Name:        "xiaotang-daozai",
		Description: "岛仔 Daozai：Rust 构建的 Windows 桌面信息岛。",
	}
	if searchScore(daozai, "DAO") == 0 {
		t.Fatalf("uppercase DAO should match daozai")
	}
	if searchScore(daozai, "dao") == 0 {
		t.Fatalf("lowercase dao should match daozai")
	}
	if searchScore(daozai, "岛仔") == 0 {
		t.Fatalf("fragment 岛仔 should match description")
	}
	if searchScore(daozai, "xiaotang") == 0 {
		t.Fatalf("partial repo name should match")
	}

	photo := catalog.RepoInfo{
		FullName:    "storyold/photocraft",
		Name:        "photocraft",
		Description: "PhotoCraft: An open-source photo editor written in pure Rust.",
	}
	if searchScore(photo, "PHOTOCRAFT") == 0 {
		t.Fatalf("case-insensitive full name should match")
	}
	if searchScore(photo, "photocraf") == 0 {
		t.Fatalf("one-character typo should fuzzy match")
	}
	if searchScore(photo, "photo") == 0 {
		t.Fatalf("prefix should match")
	}

	unrelated := catalog.RepoInfo{
		FullName:    "JudeKwong/WinIslands",
		Name:        "WinIslands",
		Description: "把 iOS 的灵动岛带到 Windows。",
	}
	if searchScore(unrelated, "岛仔") != 0 {
		t.Fatalf("single unrelated CJK fragment must not match 岛仔")
	}
	if searchScore(unrelated, "灵动") == 0 {
		t.Fatalf("partial Chinese phrase should match its description")
	}
}
