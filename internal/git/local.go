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

// DefaultIgnorePatterns are directories always ignored during scanning.
var DefaultIgnorePatterns = []string{
	".git", "node_modules", "__pycache__", ".venv", "venv",
	"vendor", "dist", "build", ".next", ".cache",
	"target", "bin", "obj", ".idea", ".vscode",
	".DS_Store", "coverage", ".mypy_cache", ".ruff_cache",
	".pytest_cache", ".tox", "gen",
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

	ignoreSet := make(map[string]bool)
	for _, p := range DefaultIgnorePatterns {
		ignoreSet[p] = true
	}

	err = filepath.Walk(absPath, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		name := fi.Name()

		// Skip ignored directories
		if fi.IsDir() {
			if ignoreSet[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip hidden files, very large files, and non-code files
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if fi.Size() > 1<<20 { // Skip files > 1MB
			return nil
		}

		lang := DetectLanguage(path)
		if lang == "" {
			return nil // Skip unknown file types
		}

		relPath, _ := filepath.Rel(absPath, path)
		repo.Files = append(repo.Files, FileInfo{
			Path:     relPath,
			AbsPath:  path,
			Language: lang,
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
