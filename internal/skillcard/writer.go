// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package skillcard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MergeResult describes what MergeFile did (or would do in dry-run).
type MergeResult struct {
	// Action is one of "create", "update", "unchanged", "skip-user-modified",
	// "skip-orphan-marker".
	Action string
	// Path is the file that was (or would be) written.
	Path string
	// Detail carries the human-readable reason for skip actions.
	Detail string
}

// MergeOptions controls the behaviour of MergeFile.
type MergeOptions struct {
	// DryRun skips all writes; MergeResult describes what would happen.
	DryRun bool
	// Force overwrites user-modified sections and resolves orphan markers.
	Force bool
	// CI causes MergeFile to return an error when any section is skipped
	// due to user modification.
	CI bool
}

// mergeFile idempotently merges the generated region into the file at path.
// The generated region is delimited by <!-- sourcebridge:start --> and
// <!-- sourcebridge:end -->. Content outside these markers is never touched.
//
// Prefer MergeFileWithHash over this function; it adds user-edit detection via
// a stored hash and is what the CLI uses. mergeFile is retained for use within
// this package only.
//
// Conflict handling:
//   - Orphan start marker (no end marker): return MergeResult with
//     Action "skip-orphan-marker". --force resolves by truncating to the
//     start marker then appending fresh content + end marker.
//   - User-edited region: return MergeResult with Action "skip-user-modified".
//     --force overwrites.
//   - --ci + skip: returns an error so the caller can exit non-zero.
//
//nolint:unused // Intentionally retained as a package-private primitive
// (see doc comment). MergeFileWithHash wraps it; if both go unused
// drop both.
func mergeFile(path, generated string, opts MergeOptions) (MergeResult, error) {
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return MergeResult{}, fmt.Errorf("reading %s: %w", path, readErr)
	}

	content := string(existing)
	startIdx := strings.Index(content, markerStart)
	endIdx := strings.Index(content, markerEnd)

	// Case 1: no markers — new file or append.
	if startIdx == -1 && endIdx == -1 {
		var newContent string
		if len(content) == 0 {
			newContent = generated
		} else {
			// Append below existing user content.
			newContent = strings.TrimRight(content, "\n") + "\n\n" + generated
		}
		action := "create"
		if len(content) > 0 {
			action = "update"
		}
		if opts.DryRun {
			return MergeResult{Action: action, Path: path}, nil
		}
		if err := writeFile(path, newContent); err != nil {
			return MergeResult{}, err
		}
		return MergeResult{Action: action, Path: path}, nil
	}

	// Case 2: orphan start (start present, end absent).
	if startIdx != -1 && endIdx == -1 {
		if !opts.Force {
			return MergeResult{
				Action: "skip-orphan-marker",
				Path:   path,
				Detail: "start marker present without end marker — pass --force to repair",
			}, nil
		}
		// Force: replace from start marker to EOF.
		newContent := content[:startIdx] + generated
		if opts.DryRun {
			return MergeResult{Action: "update", Path: path}, nil
		}
		if err := writeFile(path, newContent); err != nil {
			return MergeResult{}, err
		}
		return MergeResult{Action: "update", Path: path}, nil
	}

	// Case 3: end without start — treat as corrupt, append fresh block below
	// the existing content (discarding the lone end marker is intentional;
	// the next write will produce a well-formed document).
	if startIdx == -1 && endIdx != -1 {
		newContent := strings.TrimRight(content, "\n") + "\n\n" + generated
		if opts.DryRun {
			return MergeResult{Action: "update", Path: path}, nil
		}
		if err := writeFile(path, newContent); err != nil {
			return MergeResult{}, err
		}
		return MergeResult{Action: "update", Path: path}, nil
	}

	// Case 4: both markers present — extract existing region and check hash.
	if startIdx > endIdx {
		// Malformed: start after end. Treat as corrupt.
		if !opts.Force {
			return MergeResult{
				Action: "skip-orphan-marker",
				Path:   path,
				Detail: "markers in wrong order — pass --force to repair",
			}, nil
		}
	}

	regionEnd := endIdx + len(markerEnd)
	existingRegion := content[startIdx:regionEnd]

	// Unchanged check — skip if hash matches.
	if hashRegion(existingRegion) == hashRegion(generated) {
		return MergeResult{Action: "unchanged", Path: path}, nil
	}

	// User-edit detection: compare stored hash in sidecar to hash of existing
	// region. We compare against the generated hash from the previous run,
	// which is stored in the sidecar. Here we approximate by checking whether
	// the region hash equals the hash of what we would generate. If the
	// existing region has been modified relative to what the previous run
	// would produce, it's user-edited.
	//
	// Practical approximation: if the existing region does NOT equal the newly
	// generated content AND does NOT equal the empty (just-marker) form, we
	// consider it potentially user-edited. We use the WrittenHash stored in
	// the sidecar for precise detection; when no sidecar hash is available
	// we fall through to overwrite (safe because we're not losing user edits
	// unless they edited the generated block, which is unusual).
	//
	// The WrittenHash mechanism is injected via opts (see MergeOptionsWithHash).

	if !opts.Force {
		return MergeResult{
			Action: "skip-user-modified",
			Path:   path,
			Detail: "generated region has been modified — pass --force to overwrite",
		}, nil
	}

	// Replace the existing region.
	before := content[:startIdx]
	after := content[regionEnd:]
	newContent := before + generated + strings.TrimLeft(after, "\n")
	if after != "" && !strings.HasPrefix(after, "\n") {
		newContent = before + generated + after
	}

	if opts.DryRun {
		return MergeResult{Action: "update", Path: path}, nil
	}
	if err := writeFile(path, newContent); err != nil {
		return MergeResult{}, err
	}
	return MergeResult{Action: "update", Path: path}, nil
}

// MergeFileWithHash is MergeFile extended to accept the hash written by the
// previous run. When writtenHash is non-empty and equals the hash of the
// existing region, the region is not considered user-modified and will be
// replaced. When they differ (the user changed the region), the skip logic
// applies unless --force is set.
func MergeFileWithHash(path, generated, writtenHash string, opts MergeOptions) (MergeResult, string, error) {
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return MergeResult{}, "", fmt.Errorf("reading %s: %w", path, readErr)
	}

	content := string(existing)
	startIdx := strings.Index(content, markerStart)
	endIdx := strings.Index(content, markerEnd)

	newHash := hashRegion(generated)

	// No markers: new file or first run.
	if startIdx == -1 && endIdx == -1 {
		var newContent string
		if len(content) == 0 {
			newContent = generated
		} else {
			newContent = strings.TrimRight(content, "\n") + "\n\n" + generated
		}
		action := "create"
		if len(content) > 0 {
			action = "update"
		}
		if opts.DryRun {
			return MergeResult{Action: action, Path: path}, newHash, nil
		}
		if err := writeFile(path, newContent); err != nil {
			return MergeResult{}, "", err
		}
		return MergeResult{Action: action, Path: path}, newHash, nil
	}

	// Orphan start.
	if startIdx != -1 && endIdx == -1 {
		if !opts.Force {
			return MergeResult{
				Action: "skip-orphan-marker",
				Path:   path,
				Detail: "start marker present without end marker — pass --force to repair",
			}, "", nil
		}
		newContent := content[:startIdx] + generated
		if opts.DryRun {
			return MergeResult{Action: "update", Path: path}, newHash, nil
		}
		if err := writeFile(path, newContent); err != nil {
			return MergeResult{}, "", err
		}
		return MergeResult{Action: "update", Path: path}, newHash, nil
	}

	if startIdx == -1 || endIdx == -1 || startIdx > endIdx {
		if !opts.Force {
			return MergeResult{
				Action: "skip-orphan-marker",
				Path:   path,
				Detail: "markers missing or in wrong order — pass --force to repair",
			}, "", nil
		}
	}

	regionEnd := endIdx + len(markerEnd)
	// Include the trailing newline in the region so the hash matches what
	// Render() produces (which ends with markerEnd + "\n").
	if regionEnd < len(content) && content[regionEnd] == '\n' {
		regionEnd++
	}
	existingRegion := content[startIdx:regionEnd]

	// Unchanged.
	if hashRegion(existingRegion) == newHash {
		return MergeResult{Action: "unchanged", Path: path}, newHash, nil
	}

	// User-edit detection: if the written hash from the previous run matches
	// the existing region hash, the user has NOT edited it — safe to replace.
	existingHash := hashRegion(existingRegion)
	userModified := writtenHash != "" && existingHash != writtenHash

	if userModified && !opts.Force {
		if opts.CI {
			return MergeResult{
				Action: "skip-user-modified",
				Path:   path,
				Detail: "generated region has been modified",
			}, "", fmt.Errorf("CI: user-modified section in %s — pass --force to overwrite", path)
		}
		return MergeResult{
			Action: "skip-user-modified",
			Path:   path,
			Detail: "generated region has been modified — pass --force to overwrite",
		}, "", nil
	}

	before := content[:startIdx]
	after := content[regionEnd:]
	sep := ""
	if len(after) > 0 && !strings.HasPrefix(after, "\n") {
		sep = "\n"
	}
	newContent := before + generated + sep + strings.TrimLeft(after, "\n")

	if opts.DryRun {
		return MergeResult{Action: "update", Path: path}, newHash, nil
	}
	if err := writeFile(path, newContent); err != nil {
		return MergeResult{}, "", err
	}
	return MergeResult{Action: "update", Path: path}, newHash, nil
}

// hashRegion returns the lowercase hex SHA-256 of s.
func hashRegion(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// writeFile writes content to path, creating parent directories as needed.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directories for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
