// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"os"
	"path/filepath"
	"testing"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
)

func TestIsGitURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://github.com/user/repo.git", true},
		{"http://github.com/user/repo", true},
		{"git://github.com/user/repo", true},
		{"git@github.com:user/repo.git", true},
		{"myrepo.git", true},
		{"/home/user/project", false},
		{"./relative/path", false},
		{"", false},
	}
	for _, tc := range tests {
		got := isGitURL(tc.input)
		if got != tc.want {
			t.Errorf("isGitURL(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestSafeJoinPath(t *testing.T) {
	root := t.TempDir()

	// Valid relative path
	got, err := safeJoinPath(root, "src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(root, "src", "main.go")
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}

	// Strip leading ./
	got, err = safeJoinPath(root, "./src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}

	// Reject absolute paths
	_, err = safeJoinPath(root, "/etc/passwd")
	if err == nil {
		t.Error("expected error for absolute path")
	}

	// Reject path traversal
	_, err = safeJoinPath(root, "../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestReadSourceFile(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "src")
	os.MkdirAll(subdir, 0o755)
	os.WriteFile(filepath.Join(subdir, "main.go"), []byte("package main\n"), 0o644)

	content, err := readSourceFile(root, "src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "package main\n" {
		t.Errorf("got %q, want %q", content, "package main\n")
	}

	// Missing file
	_, err = readSourceFile(root, "nonexistent.go")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestExtractSymbolContext(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5"

	// Normal range
	got := extractSymbolContext(content, 2, 4)
	if got != "line2\nline3\nline4" {
		t.Errorf("got %q, want %q", got, "line2\nline3\nline4")
	}

	// Start before line 1
	got = extractSymbolContext(content, 0, 2)
	if got != "line1\nline2" {
		t.Errorf("got %q, want %q", got, "line1\nline2")
	}

	// End beyond file length
	got = extractSymbolContext(content, 4, 100)
	if got != "line4\nline5" {
		t.Errorf("got %q, want %q", got, "line4\nline5")
	}

	// Start beyond file length
	got = extractSymbolContext(content, 100, 200)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}

	// Single line
	got = extractSymbolContext(content, 3, 3)
	if got != "line3" {
		t.Errorf("got %q, want %q", got, "line3")
	}
}

func TestLanguageToProto(t *testing.T) {
	tests := []struct {
		input string
		want  commonv1.Language
	}{
		{"GO", commonv1.Language_LANGUAGE_GO},
		{"go", commonv1.Language_LANGUAGE_GO},
		{"Python", commonv1.Language_LANGUAGE_PYTHON},
		{"TYPESCRIPT", commonv1.Language_LANGUAGE_TYPESCRIPT},
		{"unknown", commonv1.Language_LANGUAGE_UNSPECIFIED},
		{"", commonv1.Language_LANGUAGE_UNSPECIFIED},
	}
	for _, tc := range tests {
		got := languageToProto(tc.input)
		if got != tc.want {
			t.Errorf("languageToProto(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestDeriveLanguage(t *testing.T) {
	tests := []struct {
		path string
		want commonv1.Language
	}{
		{"main.go", commonv1.Language_LANGUAGE_GO},
		{"app.py", commonv1.Language_LANGUAGE_PYTHON},
		{"index.ts", commonv1.Language_LANGUAGE_TYPESCRIPT},
		{"component.tsx", commonv1.Language_LANGUAGE_TYPESCRIPT},
		{"script.js", commonv1.Language_LANGUAGE_JAVASCRIPT},
		{"App.jsx", commonv1.Language_LANGUAGE_JAVASCRIPT},
		{"Service.java", commonv1.Language_LANGUAGE_JAVA},
		{"lib.rs", commonv1.Language_LANGUAGE_RUST},
		{"Program.cs", commonv1.Language_LANGUAGE_CSHARP},
		{"main.cpp", commonv1.Language_LANGUAGE_CPP},
		{"utils.h", commonv1.Language_LANGUAGE_CPP},
		{"app.rb", commonv1.Language_LANGUAGE_RUBY},
		{"index.php", commonv1.Language_LANGUAGE_PHP},
		{"readme.md", commonv1.Language_LANGUAGE_UNSPECIFIED},
		{"", commonv1.Language_LANGUAGE_UNSPECIFIED},
	}
	for _, tc := range tests {
		got := deriveLanguage(tc.path)
		if got != tc.want {
			t.Errorf("deriveLanguage(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestConfidenceFromFloat(t *testing.T) {
	tests := []struct {
		input float64
		want  Confidence
	}{
		{1.0, ConfidenceVerified},
		{0.95, ConfidenceHigh},
		{0.8, ConfidenceHigh},
		{0.5, ConfidenceMedium},
		{0.79, ConfidenceMedium},
		{0.3, ConfidenceLow},
		{0.0, ConfidenceLow},
	}
	for _, tc := range tests {
		got := confidenceFromFloat(tc.input)
		if got != tc.want {
			t.Errorf("confidenceFromFloat(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
