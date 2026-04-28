// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

var askImplCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask a question about your code",
	Long: `Start an interactive discussion about code using AI.

By default, sourcebridge uses the server-side orchestrator when the
configured server advertises QA capability via the /healthz response
header (X-SourceBridge-QA: v1). If the server does not advertise QA,
or when --legacy is passed, sourcebridge falls back to the local
Python subprocess. The subprocess path is the only one that can read
uncommitted working-tree changes, so --legacy remains useful for
local development.`,
	Args: cobra.ExactArgs(1),
	RunE: runAsk,
}

var (
	askRepoPath     string
	askJSON         bool
	askMode         string
	askServerURL    string
	askLegacy       bool
	askRepositoryID string
)

func init() {
	askImplCmd.Flags().StringVar(&askRepoPath, "repo", ".", "Repository path (used by --legacy)")
	askImplCmd.Flags().BoolVar(&askJSON, "json", false, "Output results as JSON")
	askImplCmd.Flags().StringVar(&askMode, "mode", "fast", "Answer mode: fast or deep")
	askImplCmd.Flags().StringVar(&askServerURL, "server", "", "SourceBridge server URL (overrides config)")
	askImplCmd.Flags().BoolVar(&askLegacy, "legacy", false, "Force the Python subprocess path (skips server capability probe)")
	askImplCmd.Flags().StringVar(&askRepositoryID, "repository-id", "", "Repository ID on the server (required for --server path)")
}

func runAsk(cmd *cobra.Command, args []string) error {
	question := args[0]

	if askMode != "fast" && askMode != "deep" {
		return fmt.Errorf("invalid ask mode %q (expected fast or deep)", askMode)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Decide path: explicit --legacy always runs the subprocess. Otherwise
	// probe /healthz for the X-SourceBridge-QA capability header. If the
	// header is present and we have a repository ID, use the server. If
	// the capability probe fails or no repository ID is available, fall
	// back silently to the subprocess so local-desktop users still get
	// working-tree answers (Ledger F13).
	if !askLegacy {
		serverURL := askServerURL
		if serverURL == "" {
			serverURL = cfg.Server.PublicBaseURL
		}
		if serverURL != "" && askRepositoryID != "" && probeQACapability(cmd.Context(), serverURL) {
			return runAskServer(cmd.Context(), serverURL, question)
		}
	}

	return runAskLegacy(cmd.Context(), cfg, question)
}

// probeQACapability does a cheap single round trip to /healthz and
// reports whether the server advertises deep-QA support. Failures
// (DNS, non-200) are treated as "no capability" without emitting
// errors — the caller will fall back to the subprocess path.
func probeQACapability(ctx context.Context, serverURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/healthz", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return resp.Header.Get("X-SourceBridge-QA") != ""
}

// runAskServer calls POST /api/v1/ask with the structured ask input
// and prints the response.
func runAskServer(ctx context.Context, serverURL, question string) error {
	payload := map[string]any{
		"repositoryId": askRepositoryID,
		"question":     question,
		"mode":         askMode,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := strings.TrimRight(serverURL, "/") + "/api/v1/ask"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := readAPIToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("server request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("server requires authentication. Run `sourcebridge setup claude --token=ca_...` or export SOURCEBRIDGE_API_TOKEN")
	}
	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, errBody.Error)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("parsing server response: %w", err)
	}
	if askJSON {
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			os.Stdout.Write(raw)
			return nil
		}
		os.Stdout.Write(buf.Bytes())
		os.Stdout.Write([]byte("\n"))
		return nil
	}
	return printAskPretty(raw)
}

// printAskPretty renders the server-side AskResult as a human-friendly
// block. Mirrors the legacy pretty output so users can't tell the
// paths apart until they look at --json.
func printAskPretty(raw json.RawMessage) error {
	var r struct {
		Answer              string `json:"answer"`
		References          []struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"references"`
		RelatedRequirements []string `json:"relatedRequirements"`
		Diagnostics         struct {
			FallbackUsed string `json:"fallbackUsed"`
			Mode         string `json:"mode"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	mode := r.Diagnostics.Mode
	if mode == "" {
		mode = askMode
	}
	fmt.Fprintf(os.Stdout, "\n[%s]\n%s\n", mode, r.Answer)
	if len(r.References) > 0 {
		fmt.Fprintf(os.Stdout, "\nReferences:\n")
		for _, ref := range r.References {
			title := ref.Title
			if title == "" {
				title = "(no title)"
			}
			fmt.Fprintf(os.Stdout, "  - [%s] %s\n", ref.Kind, title)
		}
	}
	if len(r.RelatedRequirements) > 0 {
		fmt.Fprintf(os.Stdout, "\nRelated Requirements:\n")
		for _, req := range r.RelatedRequirements {
			fmt.Fprintf(os.Stdout, "  - %s\n", req)
		}
	}
	if r.Diagnostics.FallbackUsed != "" && r.Diagnostics.FallbackUsed != "none" {
		fmt.Fprintf(os.Stdout, "\nDiagnostics:\n  - fallback: %s\n", r.Diagnostics.FallbackUsed)
	}
	return nil
}

// runAskLegacy runs the original Python subprocess path. Preserved
// so local-desktop installs continue to get working-tree answers
// (Ledger F13), and so operators can use --legacy as an emergency
// rollback.
func runAskLegacy(ctx context.Context, cfg *config.Config, question string) error {
	absRepo, err := filepath.Abs(askRepoPath)
	if err != nil {
		return fmt.Errorf("resolving repo path: %w", err)
	}

	pyCmd := exec.CommandContext(ctx, "uv", "run", "python", "cli_ask.py", question, askMode)
	pyCmd.Dir = findWorkersDir()
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

// readAPIToken reads the API token from either the
// SOURCEBRIDGE_API_TOKEN env var (takes precedence for CI / one-off
// invocations) or the canonical CLI config location. Returns empty
// string on miss — the request still goes out and the server emits
// a clear 401 if auth is required.
func readAPIToken() string {
	if t := strings.TrimSpace(os.Getenv("SOURCEBRIDGE_API_TOKEN")); t != "" {
		return t
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, ".sourcebridge", "token"),
		filepath.Join(home, ".config", "sourcebridge", "token"),
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}
