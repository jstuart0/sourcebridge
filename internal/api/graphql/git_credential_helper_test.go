// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// SEC-8 test suite — shell-injection safety of the git credential mechanism.
//
// The pre-fix implementation embedded the token in a shell function literal
// passed via GIT_CONFIG_VALUE_0:
//
//	GIT_CONFIG_VALUE_0=!f() { echo password='<token>'; }; f
//
// A token containing '; echo OWNED; ' would, in some shells, execute
// `echo OWNED` as a side effect of the shell-function evaluation.
//
// The fix (Slice 7, SEC-8) replaces this with GIT_ASKPASS pointing to a
// pre-built binary (cmd/git-credential-helper). The token is passed through
// SOURCEBRIDGE_GIT_TOKEN on cmd.Env — never on a shell command line.
//
// These tests verify:
//  1. The new mechanism sets GIT_ASKPASS (not GIT_CONFIG_VALUE_0).
//  2. The token value (including a shell-injection probe) appears verbatim
//     in SOURCEBRIDGE_GIT_TOKEN — not transformed, not shell-quoted.
//  3. No GIT_CONFIG_VALUE_0 key is present in the environment
//     (the shell-function pattern is completely removed).
//  4. gitAskpassHelper resolves the binary via the
//     SOURCEBRIDGE_GIT_ASKPASS_HELPER override (used by the test itself
//     to avoid a build dependency; the integration path is tested via
//     the Dockerfile).

package graphql

import (
	"context"
	"os"
	"strings"
	"testing"
)

// shellInjectionProbe is a classic shell-injection string.
// If this value were ever passed to a shell evaluator, `echo OWNED` would
// execute and "OWNED" would appear in output.
const shellInjectionProbe = "'; echo OWNED; '"

// TestGitPullCmdNoShellEvalInEnv proves that gitPullCmd never places the
// token inside a shell-evaluated string in cmd.Env.
//
// The test does NOT execute git; it only inspects the environment of the
// constructed exec.Cmd. This is sufficient because the security property
// we are asserting is structural: if GIT_ASKPASS points to a binary and
// the token is in a plain env var, there is no shell interpreter in the
// execution path between the token value and git.
func TestGitPullCmdNoShellEvalInEnv(t *testing.T) {
	// Point the helper resolver to a harmless stand-in (the `true` binary,
	// which always exits 0 without producing output). We only want to
	// inspect cmd.Env; git is never invoked.
	trueBin, err := findBoolBin()
	if err != nil {
		t.Skipf("skip: cannot find a dummy binary for SOURCEBRIDGE_GIT_ASKPASS_HELPER: %v", err)
	}
	t.Setenv("SOURCEBRIDGE_GIT_ASKPASS_HELPER", trueBin)

	token := shellInjectionProbe
	cmd := gitPullCmd(context.Background(), t.TempDir(), token, "")

	if cmd.Env == nil {
		t.Fatal("expected cmd.Env to be non-nil when token is provided")
	}

	// Must have GIT_ASKPASS pointing to the binary, not a shell snippet.
	askpass := envValue(cmd.Env, "GIT_ASKPASS")
	if askpass == "" {
		t.Fatal("GIT_ASKPASS must be set in cmd.Env when a token is provided")
	}
	if strings.Contains(askpass, "echo") || strings.Contains(askpass, "!f()") || strings.Contains(askpass, "password=") {
		t.Errorf("GIT_ASKPASS looks like a shell snippet, not a binary path: %q", askpass)
	}

	// Token must appear verbatim in SOURCEBRIDGE_GIT_TOKEN.
	gitToken := envValue(cmd.Env, "SOURCEBRIDGE_GIT_TOKEN")
	if gitToken != token {
		t.Errorf("SOURCEBRIDGE_GIT_TOKEN = %q; want %q", gitToken, token)
	}

	// The old shell-function pattern must be gone entirely.
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GIT_CONFIG_VALUE_0=") {
			val := strings.TrimPrefix(kv, "GIT_CONFIG_VALUE_0=")
			t.Errorf("GIT_CONFIG_VALUE_0 is still set (shell-function path not removed): %q", val)
		}
		if strings.Contains(kv, "!f()") || strings.Contains(kv, "echo password=") {
			t.Errorf("shell-function pattern detected in env: %q", kv)
		}
	}
}

// TestGitPullCmdTokenNotOnShellCommandLine proves the injection probe does
// NOT appear in the command's argument list. This is belt-and-suspenders:
// the command is `git pull --ff-only` with no token in argv at all.
func TestGitPullCmdTokenNotOnShellCommandLine(t *testing.T) {
	trueBin, err := findBoolBin()
	if err != nil {
		t.Skipf("skip: cannot find a dummy binary: %v", err)
	}
	t.Setenv("SOURCEBRIDGE_GIT_ASKPASS_HELPER", trueBin)

	cmd := gitPullCmd(context.Background(), t.TempDir(), shellInjectionProbe, "")

	for _, arg := range cmd.Args {
		if strings.Contains(arg, shellInjectionProbe) {
			t.Errorf("injection probe found in argv: %q", arg)
		}
		if strings.Contains(arg, "OWNED") {
			t.Errorf("shell-execution side-effect found in argv: %q", arg)
		}
	}
}

// TestGitPullCmdNoTokenNoCredentialEnv checks that when no token is supplied
// the credential env vars are absent entirely.
func TestGitPullCmdNoTokenNoCredentialEnv(t *testing.T) {
	cmd := gitPullCmd(context.Background(), t.TempDir(), "", "")

	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GIT_ASKPASS=") {
			t.Errorf("GIT_ASKPASS should not be set when token is empty: %q", kv)
		}
		if strings.HasPrefix(kv, "SOURCEBRIDGE_GIT_TOKEN=") {
			t.Errorf("SOURCEBRIDGE_GIT_TOKEN should not be set when token is empty: %q", kv)
		}
	}
}

// TestGitAskpassHelperOverride verifies the operator override env var takes
// priority over sibling-binary and PATH resolution.
func TestGitAskpassHelperOverride(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_GIT_ASKPASS_HELPER", "/custom/path/to/helper")
	path, ok := gitAskpassHelper()
	if !ok {
		t.Fatal("gitAskpassHelper() returned false with override set")
	}
	if path != "/custom/path/to/helper" {
		t.Errorf("gitAskpassHelper() = %q; want /custom/path/to/helper", path)
	}
}

// findBoolBin returns the path to a binary that exits 0 without output.
// Used as a stand-in for git-credential-helper in tests that only inspect
// cmd.Env and never execute git.
func findBoolBin() (string, error) {
	for _, name := range []string{"/usr/bin/true", "/bin/true", "/usr/bin/env"} {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	return "", os.ErrNotExist
}

// envValue returns the value for the given key from a "KEY=value" slice.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}
