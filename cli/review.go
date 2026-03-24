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

	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	// Check if target exists
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("cannot access %s: %w", absPath, err)
	}

	// If directory, find source files to review
	var filesToReview []string
	if info.IsDir() {
		err = filepath.Walk(absPath, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				base := filepath.Base(path)
				if base == "node_modules" || base == ".git" || base == "vendor" || base == "__pycache__" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(path)
			switch ext {
			case ".go", ".py", ".ts", ".js", ".java", ".rs", ".cs", ".cpp", ".rb":
				filesToReview = append(filesToReview, path)
			}
			return nil
		})
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
		pyCmd.Env = append(os.Environ(), "SOURCEBRIDGE_REVIEW_TEMPLATE="+reviewTemplate)

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
