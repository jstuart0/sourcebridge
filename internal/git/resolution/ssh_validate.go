// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// DefaultSSHKeyPathRoot is the canonical mount root for git SSH keys
// in the homelab + OSS Helm chart (a Kubernetes Secret projected as a
// read-only volume). Operators with a different layout pass their root
// to NewSSHKeyPathValidator. The empty string here means "use the
// homelab default" so unconfigured callers get a sensible boundary.
const DefaultSSHKeyPathRoot = "/etc/sourcebridge/git-keys"

// SSHKeyPathValidator validates an admin-supplied SSH key path against
// a configurable allow-root. The validator is configurable so tests
// can swap the root to a tempdir, and so deployments with a different
// secret-mount layout can override the default.
//
// Validation is shell-injection-aware (codex r1b High): we reject any
// character that would change the meaning of GIT_SSH_COMMAND if the
// consumer ever interpolates it into a shell string. The consumer
// SHOULD also use exec.Command argv-style invocation rather than shell
// interpolation; this validator is belt-and-suspenders.
type SSHKeyPathValidator struct {
	// AllowRoot is the directory the path must reside under.
	// Empty → DefaultSSHKeyPathRoot.
	AllowRoot string
}

// NewSSHKeyPathValidator constructs a validator with the given allow-root
// (or DefaultSSHKeyPathRoot when empty).
func NewSSHKeyPathValidator(allowRoot string) SSHKeyPathValidator {
	return SSHKeyPathValidator{AllowRoot: allowRoot}
}

// Validate enforces the SSH key path policy:
//
//  1. Empty is allowed (it means "no SSH key configured").
//  2. Otherwise the path must be absolute.
//  3. No `..` traversal segments and no redundant separators
//     (filepath.Clean(p) must equal p).
//  4. No whitespace or shell metacharacters: ; & | $ ` \\ " ' ( ) < > * \t \n \r and space.
//  5. No shell glob characters: ? [ ] { }
//  6. Must reside under AllowRoot (with normalization so /etc/foo
//     does not match /etc/foobar).
//  7. EvalSymlinks is run defensively: if the path resolves outside the
//     allow-root via a symlink, reject. Non-existent paths are accepted
//     (lazy mount: the secret may not be projected yet).
//
// The error messages are admin-facing (the REST handler shows them in
// 400 responses).
func (v SSHKeyPathValidator) Validate(p string) error {
	if p == "" {
		return nil
	}
	if !filepath.IsAbs(p) {
		return errors.New("ssh_key_path must be empty or an absolute path")
	}
	cleaned := filepath.Clean(p)
	if cleaned != p {
		return errors.New("ssh_key_path must not contain '..' or redundant separators")
	}

	// Reject whitespace + shell metacharacters with an explicit rune
	// switch (avoids the raw-string-with-backtick footgun the plan
	// pseudocode flagged at codex r1d Low).
	for _, r := range p {
		if r < 0x20 {
			return errors.New("ssh_key_path must not contain control characters")
		}
		switch r {
		case ' ', '\t', '\n', '\r',
			';', '&', '|', '$', '`', '\\', '"', '\'',
			'(', ')', '<', '>', '*':
			return errors.New("ssh_key_path must not contain whitespace or shell metacharacters")
		}
	}
	if strings.ContainsAny(p, "?[]{}") {
		return errors.New("ssh_key_path must not contain shell glob characters")
	}

	root := v.AllowRoot
	if root == "" {
		root = DefaultSSHKeyPathRoot
	}
	cleanedRoot := filepath.Clean(root)
	rootWithSep := cleanedRoot + string(filepath.Separator)
	if p != cleanedRoot && !strings.HasPrefix(p, rootWithSep) {
		return fmt.Errorf("ssh_key_path must be under %s", rootWithSep)
	}

	// Symlink check. Apply the same normalization to the resolved root
	// so /var → /private/var (macOS) doesn't spuriously fail.
	if real, err := filepath.EvalSymlinks(p); err == nil {
		realRoot, rootErr := filepath.EvalSymlinks(cleanedRoot)
		if rootErr != nil {
			realRoot = cleanedRoot
		}
		realRootWithSep := realRoot + string(filepath.Separator)
		if real != realRoot && !strings.HasPrefix(real, realRootWithSep) {
			return fmt.Errorf("ssh_key_path resolves outside the allow-root via symlink")
		}
	}
	// Non-existent path under root is allowed (lazy mount).

	return nil
}

// QuoteForShell returns a single-quoted shell-safe representation of p
// suitable for embedding into GIT_SSH_COMMAND. The validator above is
// the primary guard (it rejects every character that would matter for
// shell interpretation), but consumers SHOULD still call this helper
// when constructing GIT_SSH_COMMAND so a future loosening of the
// validator (or a path that bypassed the validator — env-bootstrap,
// legacy DB row from before R3 — codex r2) cannot inject a metachar
// into git's environment.
//
// The single-quote envelope is bash/sh-portable: inside single quotes,
// the only special character is `'` itself, which we escape via the
// standard `'\''` close-reopen idiom. Everything else (spaces,
// backticks, dollar signs, etc.) is treated literally.
//
// Example:
//
//	BuildGitSSHCommand("/etc/sourcebridge/git-keys/id_ed25519")
//	→ "ssh -i '/etc/sourcebridge/git-keys/id_ed25519' -o StrictHostKeyChecking=accept-new"
func QuoteForShell(p string) string {
	if p == "" {
		return "''"
	}
	if !strings.ContainsAny(p, "'") {
		return "'" + p + "'"
	}
	// Path contains a single-quote. Escape via close-reopen.
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// BuildGitSSHCommand returns a properly-quoted GIT_SSH_COMMAND value
// that loads the provided ssh key path. Returns the empty string when
// keyPath is empty (caller should not set GIT_SSH_COMMAND in that case).
//
// hostKeyPolicy controls the StrictHostKeyChecking option. Pass
// "accept-new" for a new clone (TOFU semantics) or "no" to skip host
// verification entirely (less safe; some legacy paths use this).
func BuildGitSSHCommand(keyPath, hostKeyPolicy string) string {
	if keyPath == "" {
		return ""
	}
	if hostKeyPolicy == "" {
		hostKeyPolicy = "accept-new"
	}
	return "ssh -i " + QuoteForShell(keyPath) + " -o StrictHostKeyChecking=" + hostKeyPolicy
}
