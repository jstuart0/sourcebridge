// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"fmt"
	"path/filepath"
	"strings"
)

// NormalizePath enforces the path-normalization contract documented in
// the plan v5 §record_change adoption posture (HIGH fix L3) and applied
// uniformly at the HTTP ingress and the in-process record_change MCP
// tool. Both surfaces call this helper before submitting a ChangeEvent;
// the router enforces the same contract via ChangeEvent.Validate (which
// reuses validateRelPath under the hood) so any out-of-band caller is
// also caught.
//
// Returns the normalized path on success, or a wrapped ErrInvalidPath on
// failure. The returned path is always:
//
//   - repo-relative
//   - Unix forward-slash separators (filepath.ToSlash applied)
//   - no leading "./" or "/"
//   - no trailing "/"
//   - no ".." traversal — neither leading, embedded, nor trailing
//   - no "//" or "/./" tricks
//
// Case-handling contract:
//
// Linux is case-sensitive at the filesystem level; macOS and Windows
// are case-insensitive by default but git treats every path
// case-sensitively regardless of platform. We therefore preserve the
// caller's casing exactly — no down-casing, no up-casing, no
// "filesystem corrects me" round-trip. A caller that submits "src/Foo.go"
// receives "src/Foo.go" back, and a caller that submits "src/foo.go"
// receives "src/foo.go" back, even when both refer to the same on-disk
// file under macOS's HFS+/APFS default-insensitive mount. The router
// validates each normalized path against the indexed repo file set
// (case-sensitive comparison, matching git's worldview); a caller that
// submits the wrong case sees rejected_invalid_paths.
//
// repoRoot is accepted but currently informational — we don't resolve
// the path against the on-disk tree here because (a) the caller may not
// have the repo materialized locally (HTTP ingress), and (b) doing
// fs-level resolution would let symlinks escape the contract under
// some obscure edge cases. The router's containment assertion catches
// any path that materializes outside the declared delta after the
// IndexFiles call returns. repoRoot is reserved for future tightening
// (e.g., an opt-in EvalSymlinks check the operator can enable).
func NormalizePath(repoRoot, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: empty path", ErrInvalidPath)
	}

	// First-pass syntactic normalization the caller MAY have already
	// done. We accept the bytes as-is and validate; we do NOT silently
	// transform a "bad" path into a "good" one (that would let bugs in
	// caller-side normalization slip through to the router).
	cleaned := filepath.ToSlash(p)
	if cleaned != p {
		return "", fmt.Errorf("%w: backslash separator in %q (callers must pre-normalize to forward-slash)", ErrInvalidPath, p)
	}

	if err := validateRelPath(cleaned); err != nil {
		return "", err
	}

	// Trailing slash is also a violation — paths name files, not directories.
	// We catch it here rather than in validateRelPath because validateRelPath
	// is called on event paths post-normalization-by-caller; this helper is
	// the canonical contract entry point.
	if strings.HasSuffix(cleaned, "/") {
		return "", fmt.Errorf("%w: trailing / in %q (paths must name a file, not a directory)", ErrInvalidPath, p)
	}

	return cleaned, nil
}

// NormalizePaths applies NormalizePath to every entry in paths and
// returns the normalized slice or the first error. Callers use this at
// the MCP tool / HTTP ingress boundaries to fail fast when any single
// path is malformed.
//
// The returned slice is a fresh copy — callers may mutate it freely.
func NormalizePaths(repoRoot string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		norm, err := NormalizePath(repoRoot, p)
		if err != nil {
			return nil, err
		}
		out = append(out, norm)
	}
	return out, nil
}
