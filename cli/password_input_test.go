// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveNonInteractive_None confirms that with no vector set the
// resolver returns ("", PasswordSourceNone, nil) so the caller can
// fall back to the interactive prompt. Pre-CA-127 this didn't exist;
// pinning the contract so a future change doesn't accidentally start
// returning an error here (which would break interactive login).
func TestResolveNonInteractive_None(t *testing.T) {
	// Make sure no env-var leaks in from the test runner.
	t.Setenv("SOURCEBRIDGE_PASSWORD", "")
	_ = os.Unsetenv("SOURCEBRIDGE_PASSWORD")

	flags := PasswordInputFlags{}
	pw, src, err := flags.ResolveNonInteractive(strings.NewReader(""))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if pw != "" {
		t.Errorf("expected empty password, got %q", pw)
	}
	if src != PasswordSourceNone {
		t.Errorf("expected PasswordSourceNone, got %v", src)
	}
}

func TestResolveNonInteractive_Stdin(t *testing.T) {
	flags := PasswordInputFlags{Stdin: true}
	pw, src, err := flags.ResolveNonInteractive(strings.NewReader("super-secret-pw\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pw != "super-secret-pw" {
		t.Errorf("password = %q, want %q", pw, "super-secret-pw")
	}
	if src != PasswordSourceStdin {
		t.Errorf("source = %v, want PasswordSourceStdin", src)
	}
}

func TestResolveNonInteractive_StdinTrimsCRLF(t *testing.T) {
	// Windows-shell users will pipe \r\n. The resolver must produce
	// the same answer as Unix \n.
	flags := PasswordInputFlags{Stdin: true}
	pw, _, err := flags.ResolveNonInteractive(strings.NewReader("pw\r\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pw != "pw" {
		t.Errorf("password = %q, want %q", pw, "pw")
	}
}

func TestResolveNonInteractive_StdinRejectsEmptyLine(t *testing.T) {
	// Empty stdin is almost certainly a pipeline mistake (the upstream
	// command produced nothing). Refusing here surfaces it sooner than
	// "invalid credentials" from the server.
	flags := PasswordInputFlags{Stdin: true}
	_, _, err := flags.ResolveNonInteractive(strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for empty stdin, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention 'empty'; got: %v", err)
	}
}

func TestResolveNonInteractive_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pw")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	flags := PasswordInputFlags{File: path}
	pw, src, err := flags.ResolveNonInteractive(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pw != "file-secret" {
		t.Errorf("password = %q, want %q", pw, "file-secret")
	}
	if src != PasswordSourceFile {
		t.Errorf("source = %v, want PasswordSourceFile", src)
	}
}

func TestResolveNonInteractive_FileMissing(t *testing.T) {
	flags := PasswordInputFlags{File: "/definitely/does/not/exist/pw"}
	_, _, err := flags.ResolveNonInteractive(nil)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "--password-file") {
		t.Errorf("error should mention --password-file; got: %v", err)
	}
}

func TestResolveNonInteractive_FileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	flags := PasswordInputFlags{File: path}
	_, _, err := flags.ResolveNonInteractive(nil)
	if err == nil {
		t.Fatal("expected error for empty file, got nil")
	}
}

func TestResolveNonInteractive_FileIsDirectory(t *testing.T) {
	flags := PasswordInputFlags{File: t.TempDir()}
	_, _, err := flags.ResolveNonInteractive(nil)
	if err == nil {
		t.Fatal("expected error for directory passed as file")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error should mention 'directory'; got: %v", err)
	}
}

func TestResolveNonInteractive_FileWarnsOnLooseMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits don't apply on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "world-readable-pw")
	if err := os.WriteFile(path, []byte("loose"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Capture stderr.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	flags := PasswordInputFlags{File: path}
	pw, _, err := flags.ResolveNonInteractive(nil)
	_ = w.Close()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pw != "loose" {
		t.Errorf("password = %q, want loose", pw)
	}

	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	stderr := string(buf[:n])
	if !strings.Contains(stderr, "warning") {
		t.Errorf("expected mode warning on stderr; got: %s", stderr)
	}
	if !strings.Contains(stderr, "0600") {
		t.Errorf("warning should suggest 0600; got: %s", stderr)
	}
}

func TestResolveNonInteractive_Env(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_PASSWORD", "env-secret")
	flags := PasswordInputFlags{}
	pw, src, err := flags.ResolveNonInteractive(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pw != "env-secret" {
		t.Errorf("password = %q, want env-secret", pw)
	}
	if src != PasswordSourceEnv {
		t.Errorf("source = %v, want PasswordSourceEnv", src)
	}
}

func TestResolveNonInteractive_EnvEmptyTreatedAsUnset(t *testing.T) {
	// SOURCEBRIDGE_PASSWORD="" must NOT engage the env vector — that's
	// a common pitfall when env vars are set unconditionally in a CI
	// matrix. Treat it as "not set" so the resolver falls back to
	// interactive (or whatever the caller's default is).
	t.Setenv("SOURCEBRIDGE_PASSWORD", "")
	flags := PasswordInputFlags{}
	_, src, err := flags.ResolveNonInteractive(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != PasswordSourceNone {
		t.Errorf("source = %v, want PasswordSourceNone", src)
	}
}

func TestResolveNonInteractive_PrecedenceConflict(t *testing.T) {
	// More than one vector engaged → refuse with a clear error. We do
	// NOT silently pick precedence here because that would mask user
	// mistakes (the most common being SOURCEBRIDGE_PASSWORD bleeding
	// in from a parent shell while the user explicitly passes
	// --password-file).
	t.Setenv("SOURCEBRIDGE_PASSWORD", "env-pw")
	dir := t.TempDir()
	path := filepath.Join(dir, "pw")
	if err := os.WriteFile(path, []byte("file-pw"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	flags := PasswordInputFlags{Stdin: true, File: path}
	_, _, err := flags.ResolveNonInteractive(strings.NewReader("stdin-pw\n"))
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "more than one") {
		t.Errorf("error should mention 'more than one'; got: %v", err)
	}
	// All three engaged sources should appear in the error so the user
	// sees exactly what to remove.
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Errorf("error should name --password-stdin; got: %v", err)
	}
	if !strings.Contains(err.Error(), "--password-file") {
		t.Errorf("error should name --password-file; got: %v", err)
	}
	if !strings.Contains(err.Error(), "SOURCEBRIDGE_PASSWORD") {
		t.Errorf("error should name SOURCEBRIDGE_PASSWORD; got: %v", err)
	}
}

func TestPasswordSource_StringLabels(t *testing.T) {
	// The labels appear in the user-facing "Using admin password from X"
	// stderr line; pin them so a refactor doesn't accidentally break
	// docs / runbook text.
	cases := map[PasswordSource]string{
		PasswordSourceNone:  "interactive",
		PasswordSourceStdin: "--password-stdin",
		PasswordSourceFile:  "--password-file",
		PasswordSourceEnv:   "SOURCEBRIDGE_PASSWORD env",
	}
	for src, want := range cases {
		if got := src.String(); got != want {
			t.Errorf("PasswordSource(%d).String() = %q, want %q", src, got, want)
		}
	}
}
