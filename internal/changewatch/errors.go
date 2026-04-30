// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sentinel errors for the router. Tests pin behavior to these via
// errors.Is so the structured-log-key contract stays stable across
// implementation changes.
var (
	// ErrEmptyDelta — guardrail #1 (non-empty delta assertion). A
	// connector that cannot determine the delta MUST NOT emit the event;
	// emitting an empty Files[] is a contract violation.
	ErrEmptyDelta = errors.New("change event: empty delta — connector contract violation")

	// ErrInvalidPath — a Files[].Path entry failed the path-normalization
	// contract. Wraps a more specific reason from validateRelPath.
	ErrInvalidPath = errors.New("change event: invalid path")

	// ErrBranchMismatch — Branch != git.HeadRef at dispatch time
	// (Risk #4 / HIGH fix #6 from the plan v5). Both branches are
	// recorded in the structured log.
	ErrBranchMismatch = errors.New("change event: branch mismatch")

	// ErrUnknownSourceKind — Source.Kind is not one of the recognized
	// 1.C values.
	ErrUnknownSourceKind = errors.New("change event: unknown source kind")

	// ErrUnknownFileStatus — a Files[].Status is not one of the four
	// recognized values.
	ErrUnknownFileStatus = errors.New("change event: unknown file status")

	// ErrUnsupportedSchemaMajor — SchemaVersion's major component is
	// outside the set the router knows how to handle (currently 0 and 1).
	ErrUnsupportedSchemaMajor = errors.New("change event: unsupported schema major version")

	// ErrContainmentViolation — the post-IndexFiles result references a
	// file outside the declared Files[] set. This is guardrail #3 from
	// the plan; in 1.C it logs and is enforced as a contract violation.
	ErrContainmentViolation = errors.New("change event: containment violation")

	// ErrChangeWatchDisabled — Submit was called but the umbrella
	// feature flag is off. Caller should drop the event quietly; the
	// router never accepts events while disabled.
	ErrChangeWatchDisabled = errors.New("change event: change-watch disabled")

	// ErrUnknownRepo — RepositoryID does not resolve to an indexed repo.
	ErrUnknownRepo = errors.New("change event: unknown repository")

	// ErrBreakerOpen — the per-repo aggregate breaker is open; events
	// are paused until the breaker recovers.
	ErrBreakerOpen = errors.New("change event: per-repo breaker open")

	// ErrRateLimited — the per-(repo, source.kind) throttle rejected
	// this event.
	ErrRateLimited = errors.New("change event: rate limited")
)

// validateRelPath enforces the path-normalization contract on a
// Files[].Path or Files[].OldPath entry.
//
// Phase 1.C: repo-relative; no leading "./" or "/"; Unix forward-slash
// separators only; no ".." components; no "//" or "/./" tricks. The
// HTTP ingress in 1.D extends this with case-sensitivity rules per
// platform; for in-process callers in 1.C we validate the normalization
// only and leave case-folding to the Watcher's path resolver, which
// already operates against the indexed-repo file set on disk.
//
// Returns nil on success; a wrapped ErrInvalidPath on failure.
func validateRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidPath)
	}
	if strings.ContainsRune(p, '\\') {
		return fmt.Errorf("%w: backslash separator in %q", ErrInvalidPath, p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: leading / in %q", ErrInvalidPath, p)
	}
	if strings.HasPrefix(p, "./") {
		return fmt.Errorf("%w: leading ./ in %q", ErrInvalidPath, p)
	}
	if strings.Contains(p, "//") {
		return fmt.Errorf("%w: double-slash in %q", ErrInvalidPath, p)
	}
	if strings.Contains(p, "/./") {
		return fmt.Errorf("%w: /./ tricks in %q", ErrInvalidPath, p)
	}
	if p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
		return fmt.Errorf("%w: .. component in %q", ErrInvalidPath, p)
	}
	return nil
}

// schemaMajor extracts the major-version integer from a "X.Y" or "X"
// schema-version string. Returns ErrUnsupportedSchemaMajor on
// malformed input so the router rejects from-the-future or garbage
// versions cheaply.
func schemaMajor(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("%w: empty schema_version", ErrUnsupportedSchemaMajor)
	}
	// Allow "0.1" / "1.0" / "2" / "0.1.0" etc.; we only need the major.
	dot := strings.IndexByte(v, '.')
	majorStr := v
	if dot >= 0 {
		majorStr = v[:dot]
	}
	n, err := strconv.Atoi(majorStr)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrUnsupportedSchemaMajor, v)
	}
	if n < 0 {
		return 0, fmt.Errorf("%w: negative major %q", ErrUnsupportedSchemaMajor, v)
	}
	return n, nil
}
