// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package git

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DiffStatus represents the type of change to a file.
type DiffStatus string

const (
	DiffAdded    DiffStatus = "added"
	DiffModified DiffStatus = "modified"
	DiffDeleted  DiffStatus = "deleted"
	DiffRenamed  DiffStatus = "renamed"
)

// FileDiff represents a file-level diff between two git refs.
type FileDiff struct {
	Path      string
	OldPath   string // set for renames
	Status    DiffStatus
	Additions int
	Deletions int
}

// DiffRefs returns file-level diffs between two git refs.
// If oldRef is empty, all files in newRef are treated as added.
func DiffRefs(repoPath, oldRef, newRef string) ([]FileDiff, error) {
	if oldRef == "" {
		return diffAllAdded(repoPath, newRef)
	}

	// Get name-status diff
	nameStatus, err := gitExec(repoPath, "diff", "--name-status", oldRef+".."+newRef)
	if err != nil {
		return nil, fmt.Errorf("git diff --name-status: %w", err)
	}

	// Get numstat diff
	numStat, err := gitExec(repoPath, "diff", "--numstat", oldRef+".."+newRef)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}

	return mergeNameStatusAndNumstat(nameStatus, numStat)
}

// diffAllAdded lists all files in a ref and marks them as added.
func diffAllAdded(repoPath, ref string) ([]FileDiff, error) {
	out, err := gitExec(repoPath, "ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %w", err)
	}
	lines := splitLines(out)
	diffs := make([]FileDiff, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		diffs = append(diffs, FileDiff{
			Path:   line,
			Status: DiffAdded,
		})
	}
	return diffs, nil
}

// mergeNameStatusAndNumstat merges --name-status and --numstat output into FileDiffs.
func mergeNameStatusAndNumstat(nameStatusOut, numStatOut string) ([]FileDiff, error) {
	// Parse name-status: "M\tfile.go", "R100\told\tnew", "A\tfile.go", "D\tfile.go"
	nameLines := splitLines(nameStatusOut)
	diffs := make([]FileDiff, 0, len(nameLines))
	pathIndex := make(map[string]int) // path -> index in diffs

	for _, line := range nameLines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		statusCode := parts[0]
		var d FileDiff

		switch {
		case statusCode == "A":
			d = FileDiff{Path: parts[1], Status: DiffAdded}
		case statusCode == "M":
			d = FileDiff{Path: parts[1], Status: DiffModified}
		case statusCode == "D":
			d = FileDiff{Path: parts[1], Status: DiffDeleted}
		case strings.HasPrefix(statusCode, "R"):
			if len(parts) < 3 {
				continue
			}
			d = FileDiff{Path: parts[2], OldPath: parts[1], Status: DiffRenamed}
		default:
			// Other statuses (C=copy, T=type-change) treated as modified
			d = FileDiff{Path: parts[len(parts)-1], Status: DiffModified}
		}

		idx := len(diffs)
		diffs = append(diffs, d)
		pathIndex[d.Path] = idx
	}

	// Parse numstat: "10\t5\tfile.go" or "10\t5\told => new"
	numLines := splitLines(numStatOut)
	for _, line := range numLines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		adds, _ := strconv.Atoi(parts[0])
		dels, _ := strconv.Atoi(parts[1])
		filePath := parts[2]

		// Handle renames in numstat: "old => new" or "{old => new}/path"
		if strings.Contains(filePath, " => ") {
			// Try to find the new path from name-status
			for path, idx := range pathIndex {
				if diffs[idx].Status == DiffRenamed {
					diffs[idx].Additions = adds
					diffs[idx].Deletions = dels
					_ = path
					break
				}
			}
			continue
		}

		if idx, ok := pathIndex[filePath]; ok {
			diffs[idx].Additions = adds
			diffs[idx].Deletions = dels
		}
	}

	return diffs, nil
}

// GetHeadCommitSHA returns the commit SHA of HEAD for the given repo path.
func GetHeadCommitSHA(repoPath string) (string, error) {
	out, err := gitExec(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func gitExec(repoPath string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%s: %s", err, string(exitErr.Stderr))
		}
		return "", err
	}
	return string(out), nil
}

func splitLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
