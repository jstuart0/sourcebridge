// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/skillcard"
	"github.com/sourcebridge/sourcebridge/internal/telemetry"
)

// setupCmd is the "setup" parent command group.
var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure SourceBridge integrations",
}

// setupClaudeCmd is the "setup claude" leaf command.
var setupClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Generate a .claude/CLAUDE.md skill card for Claude Code",
	Long: `Generate a .claude/CLAUDE.md skill card and .mcp.json configuration
for Claude Code integration. The skill card contains per-subsystem sections
derived from the repository's clustering data.

The repository must be indexed before running this command. If the server
is unreachable or the repository is not indexed, the command fails with a
clear error.`,
	RunE: runSetupClaude,
}

var (
	setupClaudeRepoID       string
	setupClaudeServer       string
	setupClaudeNoSkills     bool
	setupClaudeNoMCP        bool
	setupClaudeEnableHooks  bool
	setupClaudeDryRun       bool
	setupClaudeCI           bool
	setupClaudeForce        bool
	setupClaudeCommitConfig bool
)

func init() {
	setupClaudeCmd.Flags().StringVar(&setupClaudeRepoID, "repo-id", "", "SourceBridge repository ID (auto-detected from cwd if omitted)")
	setupClaudeCmd.Flags().StringVar(&setupClaudeServer, "server", "", "SourceBridge server URL (overrides config and SOURCEBRIDGE_URL)")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeNoSkills, "no-skills", false, "Skip generating .claude/CLAUDE.md")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeNoMCP, "no-mcp", false, "Skip generating .mcp.json")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeEnableHooks, "enable-hooks", false, "Reserved — hooks are deferred to a later milestone")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeDryRun, "dry-run", false, "Show what would be written without writing anything")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeCI, "ci", false, "Exit non-zero if any user-modified section would be skipped")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeForce, "force", false, "Overwrite user-edited sections and repair orphan markers")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeCommitConfig, "commit-config", false, "Do not add .claude/sourcebridge.json to .gitignore")

	setupCmd.AddCommand(setupClaudeCmd)
}

// clustersAPIResponse is the wire shape of GET /api/v1/repositories/{id}/clusters.
type clustersAPIResponse struct {
	RepoID      string               `json:"repo_id"`
	Status      string               `json:"status"`
	Clusters    []clusterAPISummary  `json:"clusters"`
	RetrievedAt string               `json:"retrieved_at"`
}

type clusterAPISummary struct {
	ID                    string         `json:"id"`
	Label                 string         `json:"label"`
	MemberCount           int            `json:"member_count"`
	RepresentativeSymbols []string       `json:"representative_symbols"`
	CrossClusterCalls     map[string]int `json:"cross_cluster_calls,omitempty"`
	Partial               bool           `json:"partial"`
	Packages              []string       `json:"packages,omitempty"`
	Warnings              []apiWarningDTO `json:"warnings,omitempty"`
}

// apiWarningDTO mirrors the server's warningDTO wire shape.
type apiWarningDTO struct {
	Symbol string `json:"symbol"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// capabilitiesResponse is used to check agent_setup availability.
type capabilitiesResponse struct {
	Capabilities []string `json:"capabilities"`
}

func runSetupClaude(cmd *cobra.Command, args []string) error {
	if setupClaudeEnableHooks {
		fmt.Fprintln(os.Stderr, "Note: --enable-hooks is reserved; hooks are deferred to a later milestone.")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	serverURL := resolveServerURL(cfg)
	if serverURL == "" {
		return fmt.Errorf("no SourceBridge server URL configured. Set server.public_base_url in config.toml or SOURCEBRIDGE_URL env var")
	}

	// Validate the server URL.
	parsed, err := url.Parse(serverURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("invalid server URL %q: must be http or https", serverURL)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	// Probe server reachability.
	if err := probeServerReachability(ctx, serverURL); err != nil {
		return err
	}

	// Resolve repo ID.
	repoID := setupClaudeRepoID
	if repoID == "" {
		// Look up by current working directory.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		var lookupErr error
		repoID, lookupErr = lookupRepoByPath(ctx, serverURL, cwd)
		if lookupErr != nil {
			return fmt.Errorf(
				"Error: no SourceBridge repository found for current directory.\n"+
					"       Pass --repo-id explicitly, or run `sourcebridge index` to register this repo.",
			)
		}
	}

	// Fetch cluster data.
	clusterResp, err := fetchClusters(ctx, serverURL, repoID)
	if err != nil {
		return fmt.Errorf(
			"Error: cannot reach SourceBridge server at %s.\n"+
				"       Is the server running? Check your --server flag and SOURCEBRIDGE_URL env.",
			serverURL,
		)
	}

	if clusterResp.Status == "pending" || clusterResp.Status == "unavailable" {
		return fmt.Errorf(
			"Error: repo %s hasn't been indexed yet.\n"+
				"       Wait for indexing to complete, or run `sourcebridge index` again.",
			repoID,
		)
	}

	// Sort API clusters largest-first so buildTryThisPrompt names the dominant
	// subsystems and the renderer emits them in a consistent order.
	sort.Slice(clusterResp.Clusters, func(i, j int) bool {
		return clusterResp.Clusters[i].MemberCount > clusterResp.Clusters[j].MemberCount
	})

	// Translate API clusters → skillcard.ClusterSummary.
	// Packages and Warnings are now computed server-side and returned by the API.
	clusters := make([]skillcard.ClusterSummary, 0, len(clusterResp.Clusters))
	for _, c := range clusterResp.Clusters {
		warnings := make([]skillcard.Warning, 0, len(c.Warnings))
		for _, w := range c.Warnings {
			warnings = append(warnings, skillcard.Warning{
				Symbol: w.Symbol,
				Kind:   w.Kind,
				Detail: w.Detail,
			})
		}
		clusters = append(clusters, skillcard.ClusterSummary{
			Label:              c.Label,
			MemberCount:        c.MemberCount,
			Packages:           c.Packages,
			RepresentativeSyms: c.RepresentativeSymbols,
			Warnings:           warnings,
		})
	}

	// Determine repo name from repo ID (fallback: the ID itself).
	repoName := resolveRepoName(ctx, serverURL, repoID)
	if repoName == "" {
		repoName = repoID
	}

	summary := skillcard.RepoSummary{
		RepoName:  repoName,
		RepoID:    repoID,
		ServerURL: serverURL,
		IndexedAt: parseIndexedAt(clusterResp.RetrievedAt),
		Clusters:  clusters,
	}

	// Determine base directory (where .claude/ will be written).
	baseDir := "."
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}

	// Read existing sidecar for the written hash (user-edit detection).
	existingSidecar, _ := skillcard.ReadSidecar(baseDir)
	writtenHash := ""
	if existingSidecar != nil {
		writtenHash = existingSidecar.WrittenHash
	}

	mergeOpts := skillcard.MergeOptions{
		DryRun: setupClaudeDryRun,
		Force:  setupClaudeForce,
		CI:     setupClaudeCI,
	}

	var diffActions []skillcard.DiffAction
	var newWrittenHash string

	// --- CLAUDE.md ---
	if !setupClaudeNoSkills {
		generated := skillcard.Render(summary)
		claudePath := filepath.Join(baseDir, ".claude", "CLAUDE.md")

		result, hash, mergeErr := skillcard.MergeFileWithHash(claudePath, generated, writtenHash, mergeOpts)
		if mergeErr != nil && setupClaudeCI {
			return mergeErr
		}
		newWrittenHash = hash

		tag := actionTag(result.Action)
		detail := result.Detail
		diffActions = append(diffActions, skillcard.DiffAction{
			Tag:    tag,
			Path:   ".claude/CLAUDE.md",
			Detail: detail,
		})
	}

	// --- .mcp.json ---
	if !setupClaudeNoMCP {
		mcpPath := filepath.Join(baseDir, ".mcp.json")
		if setupClaudeDryRun {
			mcpTag := dryRunMCPTag(mcpPath, serverURL)
			diffActions = append(diffActions, skillcard.DiffAction{
				Tag:  mcpTag,
				Path: ".mcp.json",
			})
		} else {
			_, warn, mcpErr := skillcard.MergeMCPJSON(mcpPath, serverURL, repoID, setupClaudeForce)
			if mcpErr != nil {
				return mcpErr
			}
			if warn != "" {
				fmt.Fprintln(os.Stderr, "Warning:", warn)
			}
			mcpTag := dryRunMCPTag(mcpPath, serverURL)
			diffActions = append(diffActions, skillcard.DiffAction{
				Tag:  mcpTag,
				Path: ".mcp.json",
			})
		}
	}

	// --- .claude/sourcebridge.json sidecar ---
	sidecarRelPath := filepath.Join(".claude", "sourcebridge.json")
	indexedAt := summary.IndexedAt
	if indexedAt.IsZero() {
		indexedAt = time.Now().UTC()
	}

	newSidecar := &skillcard.Sidecar{
		RepoID:         repoID,
		ServerURL:      serverURL,
		LastIndexAt:    indexedAt.UTC().Format(time.RFC3339),
		GeneratedFiles: []string{".claude/CLAUDE.md"},
		WrittenHash:    newWrittenHash,
	}
	sidecarTag := dryRunSidecarTag(baseDir, repoID, serverURL)
	if setupClaudeDryRun {
		diffActions = append(diffActions, skillcard.DiffAction{
			Tag:  sidecarTag,
			Path: sidecarRelPath,
		})
	} else {
		if err := skillcard.WriteSidecar(baseDir, newSidecar); err != nil {
			return fmt.Errorf("writing sidecar: %w", err)
		}
		diffActions = append(diffActions, skillcard.DiffAction{
			Tag:  sidecarTag,
			Path: sidecarRelPath,
		})
	}

	// --- .gitignore patch ---
	if !setupClaudeCommitConfig {
		gitignorePath := filepath.Join(baseDir, ".gitignore")
		gitignoreEntry := ".claude/sourcebridge.json"
		if setupClaudeDryRun {
			diffActions = append(diffActions, skillcard.DiffAction{
				Tag:    "MODIFY",
				Path:   ".gitignore",
				Detail: "(+1 line: " + gitignoreEntry + ")",
			})
		} else {
			changed, err := skillcard.PatchGitignore(gitignorePath, gitignoreEntry)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not patch .gitignore: %v\n", err)
			} else if changed {
				diffActions = append(diffActions, skillcard.DiffAction{
					Tag:    "MODIFY",
					Path:   ".gitignore",
					Detail: "(+1 line: " + gitignoreEntry + ")",
				})
			}
		}
	}

	if setupClaudeDryRun {
		skillcard.PrintDiff(os.Stdout, diffActions)
		return nil
	}

	// Record that setup claude has been run for telemetry.
	dataDir := cfg.Storage.RepoCachePath
	if dataDir == "" {
		dataDir = "./repo-cache"
	}
	telemetry.MarkAgentSetupUsed(dataDir)

	// Non-dry-run: print a terse success summary.
	fmt.Fprintf(os.Stdout, "SourceBridge skill card written to .claude/CLAUDE.md\n")
	fmt.Fprintf(os.Stdout, "Run in Claude Code: \"List the subsystems of this repo.\"\n")

	return nil
}

// resolveServerURL picks the server URL from flag → env → config (in that order).
func resolveServerURL(cfg *config.Config) string {
	if setupClaudeServer != "" {
		return strings.TrimRight(setupClaudeServer, "/")
	}
	if env := os.Getenv("SOURCEBRIDGE_URL"); env != "" {
		return strings.TrimRight(env, "/")
	}
	return strings.TrimRight(cfg.Server.PublicBaseURL, "/")
}

// probeServerReachability verifies the server is reachable by calling /healthz.
// A network error or 5xx response is treated as unreachable. The 403 capability
// check happens later when fetchClusters is called.
func probeServerReachability(ctx context.Context, serverURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/healthz", nil)
	if err != nil {
		return fmt.Errorf(
			"Error: cannot reach SourceBridge server at %s.\n"+
				"       Is the server running? Check your --server flag and SOURCEBRIDGE_URL env.",
			serverURL,
		)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf(
			"Error: cannot reach SourceBridge server at %s.\n"+
				"       Is the server running? Check your --server flag and SOURCEBRIDGE_URL env.",
			serverURL,
		)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Older server without /healthz — proceed to the API.
		return nil
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf(
			"Error: cannot reach SourceBridge server at %s.\n"+
				"       Is the server running? Check your --server flag and SOURCEBRIDGE_URL env.",
			serverURL,
		)
	}
	return nil
}

// fetchClusters calls GET /api/v1/repositories/{repo_id}/clusters.
func fetchClusters(ctx context.Context, serverURL, repoID string) (*clustersAPIResponse, error) {
	endpoint := serverURL + "/api/v1/repositories/" + repoID + "/clusters"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if token := readAPIToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("this SourceBridge instance doesn't expose the agent_setup capability. Update or contact your admin")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clusters API returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result clustersAPIResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing clusters response: %w", err)
	}
	return &result, nil
}

// lookupRepoByPath tries to find a repository by its local path via the REST API.
// Returns the repo ID on success.
func lookupRepoByPath(ctx context.Context, serverURL, localPath string) (string, error) {
	endpoint := serverURL + "/api/v1/repositories"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	if token := readAPIToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("repositories API returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var repos []struct {
		ID   string `json:"id"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &repos); err != nil {
		return "", fmt.Errorf("parsing repositories: %w", err)
	}
	// Match on path prefix or exact repo name.
	for _, r := range repos {
		if r.Path == localPath || strings.HasPrefix(localPath, r.Path) {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("no indexed repository found matching path %s", localPath)
}

// resolveRepoName fetches the repository name from the server.
func resolveRepoName(ctx context.Context, serverURL, repoID string) string {
	endpoint := serverURL + "/api/v1/repositories/" + repoID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	if token := readAPIToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	var repo struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &repo); err != nil {
		return ""
	}
	return repo.Name
}

// dryRunMCPTag determines the appropriate DiffAction tag for .mcp.json by
// inspecting the existing file without writing anything.
//
// Detection matches the HTTP-transport shape that MergeMCPJSON now writes:
//   - entry["type"] == "http" AND entry["url"] == expectedURL → UNCHANGED
//   - file absent → CREATE
//   - any other state (missing, broken JSON, old stdio shape, wrong URL) → MODIFY
func dryRunMCPTag(mcpPath, serverURL string) string {
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		// File absent.
		return "CREATE"
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "MODIFY"
	}
	servers, _ := doc["mcpServers"].(map[string]interface{})
	if servers == nil {
		return "MODIFY"
	}
	entry, _ := servers["sourcebridge"].(map[string]interface{})
	if entry == nil {
		return "MODIFY"
	}
	// Detect the HTTP-transport shape written by the current MergeMCPJSON.
	expectedURL := skillcard.MCPExpectedURL(serverURL)
	if entry["type"] == "http" && entry["url"] == expectedURL {
		return "UNCHANGED"
	}
	return "MODIFY"
}

// dryRunSidecarTag determines the appropriate DiffAction tag for the sidecar
// by inspecting its current state.
func dryRunSidecarTag(baseDir, repoID, serverURL string) string {
	existing, err := skillcard.ReadSidecar(baseDir)
	if err != nil || existing == nil {
		return "CREATE"
	}
	if existing.RepoID == repoID && existing.ServerURL == serverURL {
		return "MODIFY" // re-run: same repo, updating last_index_at / hash
	}
	return "MODIFY"
}

// parseIndexedAt parses an RFC3339 timestamp string, returning zero time on failure.
func parseIndexedAt(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// actionTag converts a MergeResult Action string to a DiffAction Tag.
func actionTag(action string) string {
	switch action {
	case "create":
		return "CREATE"
	case "update":
		return "MODIFY"
	case "unchanged":
		return "UNCHANGED"
	case "skip-user-modified":
		return "SKIP — user-modified"
	case "skip-orphan-marker":
		return "SKIP — orphan marker"
	default:
		return strings.ToUpper(action)
	}
}
