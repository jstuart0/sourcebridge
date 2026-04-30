// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FileInfo represents a file in a repository.
type FileInfo struct {
	Path     string // Relative to repo root
	AbsPath  string
	Language string
	Size     int64
}

// RepoInfo contains metadata about a repository.
type RepoInfo struct {
	Name    string
	Path    string // Absolute path
	Files   []FileInfo
}

// DefaultIgnorePatterns are directory names always ignored during scanning.
//
// The set is exposed so the change-watch pipeline (Phase 1.A — see
// thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md) and any
// future scanner can share one source of truth for ignored paths.
// Modify with care: every entry change is observable to existing
// indexer behavior.
var DefaultIgnorePatterns = []string{
	".git", "node_modules", "__pycache__", ".venv", "venv",
	"vendor", "dist", "build", ".next", ".cache",
	"target", "bin", "obj", ".idea", ".vscode",
	".DS_Store", "coverage", ".mypy_cache", ".ruff_cache",
	".pytest_cache", ".tox", "gen",
}

// defaultIgnoreSet is the precomputed lookup for DefaultIgnorePatterns.
// Built once at package init so callers on the hot path
// (filepath.Walk callbacks, fsnotify event handlers) avoid rebuilding
// the map on every call.
var defaultIgnoreSet = func() map[string]bool {
	s := make(map[string]bool, len(DefaultIgnorePatterns))
	for _, p := range DefaultIgnorePatterns {
		s[p] = true
	}
	return s
}()

// IsIgnoredPath returns true if relPath (a forward-slash, repo-relative
// path) should be skipped by the scanner under the same rules
// ScanRepository applies inline today.
//
// The decision rule, in order:
//  1. Any path whose any component matches DefaultIgnorePatterns is
//     ignored (e.g. "vendor/foo/bar.go", "node_modules/x").
//  2. Any path whose any component begins with "." (other than the
//     root ".") is ignored (matches ScanRepository's hidden-dir and
//     hidden-file rules: "src/.cache/foo", ".github/workflows/x.yml",
//     ".env").
//  3. Files whose extension is not a recognized language (per
//     DetectLanguage) are ignored.
//
// File-size thresholds (1 MiB) are NOT enforced here because that
// requires a stat call the helper's caller may not need; ScanRepository
// continues to apply that filter inline. The watcher's caller will
// stat once for its own reasons and apply the same threshold there.
//
// Path contract: relPath must be forward-slash, repo-relative. The
// HTTP ingress (Phase 1.D) and the in-process watcher (Phase 1.C)
// enforce this at their boundaries; the helper does not re-validate
// because doing so would be ambiguous on Unix where backslash is a
// legitimate filename byte. A leading "./" is tolerated.
//
// repoPath is currently unused but kept in the signature for future
// extension (per-repo .gitignore parsing, custom workspace-level
// ignore overrides). Callers should pass the repo root unconditionally
// so the signature does not need to churn later.
func IsIgnoredPath(repoPath, relPath string) bool {
	_ = repoPath // reserved; see godoc

	clean := strings.TrimPrefix(relPath, "./")
	if clean == "" || clean == "." {
		return false
	}
	parts := strings.Split(clean, "/")

	// Component-by-component: any ignored or hidden component
	// disqualifies the whole path. This matches ScanRepository's
	// behavior — once filepath.Walk hits an ignored or hidden
	// directory, SkipDir prunes the entire subtree.
	for i, part := range parts {
		if part == "" {
			// Empty segment from a "//" path; treat as benign.
			continue
		}
		if defaultIgnoreSet[part] {
			return true
		}
		// Hidden-directory rule: any non-leaf component starting with
		// "." is treated as a hidden directory.
		if i < len(parts)-1 && strings.HasPrefix(part, ".") {
			return true
		}
		// Hidden-file rule: a leaf component starting with "." is a
		// hidden file (matches the strings.HasPrefix(name, ".") check
		// in ScanRepository's file branch).
		if i == len(parts)-1 && strings.HasPrefix(part, ".") {
			return true
		}
	}

	// Unknown-language rule: the leaf must have a recognized
	// extension, mirroring ScanRepository's `DetectLanguage(path) == ""`
	// skip.
	leaf := parts[len(parts)-1]
	if DetectLanguage(leaf) == "" {
		return true
	}

	return false
}

// ScanRepository walks a local repository path and returns file information.
func ScanRepository(rootPath string) (*RepoInfo, error) {
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolving path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("accessing path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", absPath)
	}

	repo := &RepoInfo{
		Name: filepath.Base(absPath),
		Path: absPath,
	}

	err = filepath.Walk(absPath, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		name := fi.Name()

		// Skip ignored directories. Use the shared helper for the
		// component-name rules so the watcher in Phase 1.C
		// (internal/changewatch) and any other future scanner can
		// reuse identical filtering semantics. We still need the
		// directory short-circuit (filepath.SkipDir) here because
		// IsIgnoredPath answers per-path, not per-walk-step.
		if fi.IsDir() {
			if defaultIgnoreSet[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		// File path filtering goes through the shared helper. The
		// helper handles hidden-file and unknown-language rules.
		// Paths from filepath.Rel use the OS separator; convert to
		// the helper's forward-slash contract.
		relPath, _ := filepath.Rel(absPath, path)
		if IsIgnoredPath(absPath, filepath.ToSlash(relPath)) {
			return nil
		}

		// Size threshold stays here; IsIgnoredPath deliberately does
		// not stat (see godoc).
		if fi.Size() > 1<<20 { // Skip files > 1MB
			return nil
		}

		repo.Files = append(repo.Files, FileInfo{
			Path:     relPath,
			AbsPath:  path,
			Language: DetectLanguage(path),
			Size:     fi.Size(),
		})

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning repository: %w", err)
	}

	return repo, nil
}

// ReadFile reads the content of a file.
func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// GitMetadata contains git repository metadata.
type GitMetadata struct {
	CommitSHA string
	Branch    string
	ParentSHA string
}

// ErrNotAGitRepo is returned by HeadRef when repoPath is not a git
// working tree. GetGitMetadata signals the same condition by returning
// (nil, nil) — HeadRef wraps that case as an error so callers like the
// change-watch router (Phase 1.C) get an unambiguous failure instead
// of an empty branch string they have to second-guess.
var ErrNotAGitRepo = fmt.Errorf("not a git repository")

// HeadRef returns the symbolic branch name (`git rev-parse --abbrev-ref HEAD`)
// of repoPath's working tree, e.g. "main" or "feature/x".
//
// On a detached HEAD git returns "HEAD", which HeadRef passes through
// unchanged — callers that need to reject detached state must do so
// at their own layer.
//
// Errors:
//   - ErrNotAGitRepo if repoPath has no .git entry.
//   - Wrapped exec error from `git rev-parse` if the command fails for
//     any other reason (e.g. broken git index, missing binary).
//
// HeadRef is the load-bearing branch source for the change-watch
// pipeline (Phase 1.C router validation, Phase 1.B IndexFiles
// recording). The wrapper is deliberately thin so future migrations
// to gogit or libgit2 stay confined to GetGitMetadata.
func HeadRef(repoPath string) (string, error) {
	meta, err := GetGitMetadata(repoPath)
	if err != nil {
		return "", fmt.Errorf("reading git metadata: %w", err)
	}
	if meta == nil {
		return "", ErrNotAGitRepo
	}
	return meta.Branch, nil
}

// GetGitMetadata extracts git metadata from a repository path.
// Returns nil without error if the path is not a git repository.
func GetGitMetadata(repoPath string) (*GitMetadata, error) {
	// Check if it's a git repo
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); os.IsNotExist(err) {
		return nil, nil
	}

	meta := &GitMetadata{}

	// git rev-parse HEAD
	if out, err := runGit(repoPath, "rev-parse", "HEAD"); err == nil {
		meta.CommitSHA = strings.TrimSpace(out)
	}

	// git rev-parse --abbrev-ref HEAD
	if out, err := runGit(repoPath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		meta.Branch = strings.TrimSpace(out)
	}

	// git log -1 --format=%P (parent SHA)
	if out, err := runGit(repoPath, "log", "-1", "--format=%P"); err == nil {
		fields := strings.Fields(strings.TrimSpace(out))
		if len(fields) > 0 {
			meta.ParentSHA = fields[0]
		}
	}

	return meta, nil
}

func runGit(repoPath string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// DetectLanguage returns the language name for a file based on its extension.
func DetectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescript"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascript"
	case ".java":
		return "java"
	case ".rs":
		return "rust"
	case ".cs":
		return "csharp"
	case ".cpp", ".cc", ".cxx", ".c", ".h", ".hpp":
		return "cpp"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".csv":
		return "csv"
	default:
		return ""
	}
}
