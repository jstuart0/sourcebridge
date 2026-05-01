// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/git"
)

var reviewImplCmd = &cobra.Command{
	Use:   "review [path]",
	Short: "Run a structured code review",
	Long:  "Analyze code for security, SOLID, performance, reliability, and maintainability issues.",
	Args:  cobra.ExactArgs(1),
	RunE:  runReview,
}

var (
	reviewTemplate string
	reviewRepoPath string
	reviewJSON     bool
)

func init() {
	reviewImplCmd.Flags().StringVar(&reviewTemplate, "template", "security", "Review template (security, solid, performance, reliability, maintainability)")
	reviewImplCmd.Flags().StringVar(&reviewRepoPath, "repo", ".", "Repository path")
	reviewImplCmd.Flags().BoolVar(&reviewJSON, "json", false, "Output results as JSON")
}

func runReview(cmd *cobra.Command, args []string) error {
	targetPath := args[0]
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	// Check if target exists
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("cannot access %s: %w", absPath, err)
	}

	// If directory, find source files to review.
	var filesToReview []string
	if info.IsDir() {
		filesToReview, err = findReviewableFiles(absPath)
		if err != nil {
			return fmt.Errorf("walking directory: %w", err)
		}
	} else {
		filesToReview = []string{absPath}
	}

	if len(filesToReview) == 0 {
		fmt.Fprintln(os.Stderr, "No source files found to review.")
		return nil
	}

	// Try to invoke Python worker
	var allFindings []map[string]interface{}
	for _, file := range filesToReview {
		pyCmd := exec.CommandContext(cmd.Context(), "uv", "run", "python", "cli_review.py", file)
		pyCmd.Dir = findWorkersDir()
		pyCmd.Env = append(os.Environ(), buildWorkerLLMEnv(cfg, cfg.LLM.ReviewModel, "SOURCEBRIDGE_LLM_REVIEW_MODEL")...)
		pyCmd.Env = append(pyCmd.Env, "SOURCEBRIDGE_REVIEW_TEMPLATE="+reviewTemplate)

		output, err := pyCmd.Output()
		if err != nil {
			// Graceful degradation: Python worker unavailable
			fmt.Fprintf(os.Stderr, "Error: Python AI worker unavailable for review.\n")
			fmt.Fprintf(os.Stderr, "Install the Python worker: cd workers && uv sync\n")
			return fmt.Errorf("python worker required for code reviews")
		}

		var result map[string]interface{}
		if err := json.Unmarshal(output, &result); err != nil {
			continue
		}

		if findings, ok := result["findings"].([]interface{}); ok {
			for _, f := range findings {
				if fm, ok := f.(map[string]interface{}); ok {
					allFindings = append(allFindings, fm)
				}
			}
		}
	}

	output := map[string]interface{}{
		"template": reviewTemplate,
		"findings": allFindings,
		"files":    len(filesToReview),
	}

	if reviewJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	fmt.Fprintf(os.Stdout, "\nReview: %s template\n", reviewTemplate)
	fmt.Fprintf(os.Stdout, "Files:  %d\n", len(filesToReview))
	fmt.Fprintf(os.Stdout, "Findings: %d\n\n", len(allFindings))

	for _, f := range allFindings {
		severity := f["severity"]
		message := f["message"]
		filePath := f["file_path"]
		fmt.Fprintf(os.Stdout, "  [%s] %s — %s\n", severity, filePath, message)
		if suggestion, ok := f["suggestion"]; ok && suggestion != "" {
			fmt.Fprintf(os.Stdout, "         Fix: %s\n", suggestion)
		}
	}

	return nil
}

// reviewableExtensions lists the file extensions the review walker
// considers source code worth sending to the reviewer. Lower-case,
// dotted (".go", ".py", ...). Kept package-private so the walker and
// its tests share one source of truth.
var reviewableExtensions = map[string]bool{
	".go":   true,
	".py":   true,
	".ts":   true,
	".js":   true,
	".java": true,
	".rs":   true,
	".cs":   true,
	".cpp":  true,
	".rb":   true,
}

// findReviewableFiles walks rootDir and returns every file whose
// extension is recognized as reviewable source code. Directories are
// pruned via git.IsIgnoredDir, which is driven by
// git.DefaultIgnorePatterns — the same source of truth the indexer
// and change-watch walkers consume. This means a single addition
// (e.g. ".parcel-cache") in DefaultIgnorePatterns picks up here
// automatically; the walker has no parallel skip list of its own.
//
// rootDir must be an absolute path to a directory; behavior on a
// relative path or a file is undefined (callers stat first). The
// returned paths are absolute.
//
// Tester report 2026-04-30 (Pazaryna) Issue 6 / CA-124: extracted from
// runReview so the walker is unit-testable without standing up a
// Python worker subprocess.
func findReviewableFiles(rootDir string) ([]string, error) {
	var files []string
	err := filepath.Walk(rootDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			// Permission errors and the like are ignored — matches the
			// pre-extraction behavior so existing users see no change.
			return nil
		}
		if fi.IsDir() {
			// Never ignore the root itself; otherwise a user passing a
			// directory whose name happens to match the ignore set
			// (e.g. `sourcebridge review ./dist`) would get an empty
			// result with no warning.
			if path == rootDir {
				return nil
			}
			relDir, relErr := filepath.Rel(rootDir, path)
			if relErr != nil {
				// Defensive: Rel against a descendant of rootDir
				// shouldn't fail. If it does, surface no skip so we
				// don't accidentally prune the user's repo.
				return nil
			}
			if git.IsIgnoredDir(rootDir, filepath.ToSlash(relDir)) {
				return filepath.SkipDir
			}
			return nil
		}
		if reviewableExtensions[filepath.Ext(path)] {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func findWorkersDir() string {
	// Look relative to the binary or CWD
	candidates := []string{
		"workers",
		"../workers",
		filepath.Join(os.Getenv("SOURCEBRIDGE_ROOT"), "workers"),
	}
	for _, c := range candidates {
		abs, _ := filepath.Abs(c)
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			return abs
		}
	}
	return "workers"
}
