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
)

var askImplCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask a question about your code",
	Long:  "Start an interactive discussion about code using AI.",
	Args:  cobra.ExactArgs(1),
	RunE:  runAsk,
}

var (
	askRepoPath string
	askJSON     bool
	askMode     string
)

func init() {
	askImplCmd.Flags().StringVar(&askRepoPath, "repo", ".", "Repository path")
	askImplCmd.Flags().BoolVar(&askJSON, "json", false, "Output results as JSON")
	askImplCmd.Flags().StringVar(&askMode, "mode", "fast", "Answer mode: fast or deep")
}

func runAsk(cmd *cobra.Command, args []string) error {
	question := args[0]

	absRepo, err := filepath.Abs(askRepoPath)
	if err != nil {
		return fmt.Errorf("resolving repo path: %w", err)
	}

	if askMode != "fast" && askMode != "deep" {
		return fmt.Errorf("invalid ask mode %q (expected fast or deep)", askMode)
	}

	pyCmd := exec.CommandContext(cmd.Context(), "uv", "run", "python", "cli_ask.py", question, askMode)
	pyCmd.Dir = findWorkersDir()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	pyCmd.Env = append(os.Environ(), buildWorkerLLMEnv(cfg, cfg.LLM.AskModel, "SOURCEBRIDGE_LLM_ASK_MODEL")...)
	pyCmd.Env = append(pyCmd.Env, "SOURCEBRIDGE_REPO_PATH="+absRepo)

	output, err := pyCmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Python AI worker unavailable for code discussion.\n")
		fmt.Fprintf(os.Stderr, "Install the Python worker: cd workers && uv sync\n")
		return fmt.Errorf("python worker required for code discussion")
	}

	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("parsing worker response: %w", err)
	}

	if askJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	answer := result["answer"]
	fmt.Fprintf(os.Stdout, "\n[%s]\n%s\n", askMode, answer)

	if refs, ok := result["references"].([]interface{}); ok && len(refs) > 0 {
		fmt.Fprintf(os.Stdout, "\nReferences:\n")
		for _, r := range refs {
			fmt.Fprintf(os.Stdout, "  - %s\n", r)
		}
	}

	if reqs, ok := result["related_requirements"].([]interface{}); ok && len(reqs) > 0 {
		fmt.Fprintf(os.Stdout, "\nRelated Requirements:\n")
		for _, r := range reqs {
			fmt.Fprintf(os.Stdout, "  - %s\n", r)
		}
	}

	if diagnostics, ok := result["diagnostics"].(map[string]interface{}); ok {
		if fallback, ok := diagnostics["fallback_used"].(string); ok && fallback != "" && fallback != "none" {
			fmt.Fprintf(os.Stdout, "\nDiagnostics:\n  - fallback: %s\n", fallback)
		}
	}

	return nil
}
