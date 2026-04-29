// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"encoding/json"
	"errors"
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

	// Token flags (slice 2).
	setupClaudeToken      string
	setupClaudeNoSave     bool
	setupClaudeForceToken bool

	// Proxy entry portability (slice 3 of cli-mcp-proxy-and-installer).
	// When true, the .mcp.json `command` is the bare string "sourcebridge"
	// (relies on PATH); when false (default) it is the absolute path of
	// the running binary (works regardless of GUI-launched PATH).
	setupClaudePortableCommand bool
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
	setupClaudeCmd.Flags().StringVar(&setupClaudeToken, "token", "", "SourceBridge API token (use --token=ca_... or set SOURCEBRIDGE_API_TOKEN)")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeNoSave, "no-save", false, "Don't persist --token to ~/.sourcebridge/token (default: persist)")
	setupClaudeCmd.Flags().BoolVar(&setupClaudeForceToken, "force-token", false, "Overwrite an existing ~/.sourcebridge/token with --token")
	setupClaudeCmd.Flags().BoolVar(&setupClaudePortableCommand, "portable-command", false,
		"Write .mcp.json `command` as the bare string \"sourcebridge\" (relies on PATH) "+
			"instead of the absolute path of the running binary. Use for committed/team-shared .mcp.json files "+
			"where teammates may install the CLI to a different location.")

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

// sanitizeStatusBody bounds and sanitizes a non-2xx HTTP response body
// before it is printed to the user's terminal. Caps at 512 bytes,
// strips control chars (other than newline/tab), and replaces
// non-ASCII bytes with '?' so a malicious server cannot inject ANSI
// escapes or terminal-confusing characters via an error message.
func sanitizeStatusBody(raw string) string {
	const maxLen = 512
	truncated := false
	if len(raw) > maxLen {
		raw = raw[:maxLen]
		truncated = true
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, c := range []byte(raw) {
		switch {
		case c == '\n' || c == '\t':
			b.WriteByte(c)
		case c < 0x20 || c > 0x7E:
			b.WriteByte('?')
		default:
			b.WriteByte(c)
		}
	}
	out := strings.TrimSpace(b.String())
	if truncated {
		out += "… [truncated]"
	}
	return out
}

// httpStatusError is returned by fetchClusters and the resolveToken probe when
// the HTTP call completed (no transport error) but the server replied with a
// non-success status. The top-level error handler uses errors.As to detect this
// and suppresses the "is the server running?" framing, which only applies to
// genuine transport failures.
type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("server returned HTTP %d: %s", e.status, e.body)
}

// resolveToken determines the API token to use for this invocation and,
// when --token is provided, validates it with a probe and (unless --no-save)
// persists it to ~/.sourcebridge/token.
//
// Resolution order intentionally puts --token first, ahead of SOURCEBRIDGE_API_TOKEN.
// This diverges from readAPIToken() which puts the env var first. The difference
// is intentional: an explicit flag passed at the command line is the most
// deliberate signal and must win over a previously-set env var. readAPIToken()
// exists for passive/background resolution where no flag was given.
func resolveToken(ctx context.Context, serverURL string) (string, error) {
	// 1. --token flag (highest precedence).
	if flag := strings.TrimSpace(setupClaudeToken); flag != "" {
		if err := validateAndPersistToken(ctx, serverURL, flag); err != nil {
			return "", err
		}
		return flag, nil
	}
	// 2–4. Fall back to the existing resolution chain (env → ~/.sourcebridge/token
	// → ~/.config/sourcebridge/token). See readAPIToken in token_storage.go.
	return readAPIToken(), nil
}

// validateAndPersistToken probes /api/v1/repositories with the given token.
// On success (200) it persists the token to ~/.sourcebridge/token unless
// --no-save was set. The token is NEVER written on a non-200 response.
func validateAndPersistToken(ctx context.Context, serverURL, token string) error {
	endpoint := serverURL + "/api/v1/repositories"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Transport failure — bubble up unwrapped so the caller's "cannot reach
		// server" framing applies.
		return err
	}
	defer resp.Body.Close()
	rawBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		// Token is valid. Persist unless --no-save.
		if !setupClaudeNoSave {
			result, err := saveToken(token, setupClaudeForceToken)
			if err != nil {
				return err
			}
			if result.Written {
				if result.Replaced {
					fmt.Fprintf(os.Stdout, "Saved token to ~/.sourcebridge/token (0600, overwriting previous)\n")
				} else {
					fmt.Fprintf(os.Stdout, "Saved token to ~/.sourcebridge/token (0600)\n")
				}
			}
		}
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf(
			"Error: the token you provided is invalid or revoked.\n"+
				"       Mint a new one at %s/settings/tokens and retry.",
			serverURL,
		)
	case http.StatusForbidden:
		return fmt.Errorf(
			"Error: the token you provided does not have permission to read repositories on this server.",
		)
	default:
		return &httpStatusError{status: resp.StatusCode, body: sanitizeStatusBody(string(rawBody))}
	}
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

	// Warn when --token is used: the value is visible in process listings
	// (/proc/<pid>/cmdline on Linux, `ps` output on macOS). This warning is
	// intentional friction — users on shared or multi-user systems need it even
	// more than interactive users, so it is not suppressed by --ci.
	if strings.TrimSpace(setupClaudeToken) != "" {
		fmt.Fprintln(os.Stderr,
			"Note: --token values are visible in process listings (e.g. /proc/<pid>/cmdline). "+
				"For more secure alternatives use SOURCEBRIDGE_API_TOKEN or `sourcebridge login`.")
	}

	// Resolve and (if --token was passed) validate + persist the API token.
	// resolveToken() emits its own actionable errors for 401/403 from the probe,
	// so we return those directly without the generic "cannot reach server" wrapper.
	if _, err := resolveToken(ctx, serverURL); err != nil {
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
		// httpStatusError means the server was reachable but returned a bad status.
		// Return those errors (which carry their own actionable messages) directly —
		// the "is the server running?" framing is wrong for a server that replied.
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) {
			return err
		}
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
			_, warn, mcpErr := skillcard.MergeMCPJSON(mcpPath, serverURL, repoID, setupClaudePortableCommand, setupClaudeForce)
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

// resolveServerURL picks the server URL from: --server flag → SOURCEBRIDGE_URL
// env → ~/.sourcebridge/server (written by `sourcebridge login`) → config.toml.
func resolveServerURL(cfg *config.Config) string {
	if setupClaudeServer != "" {
		return strings.TrimRight(setupClaudeServer, "/")
	}
	if env := os.Getenv("SOURCEBRIDGE_URL"); env != "" {
		return strings.TrimRight(env, "/")
	}
	if saved := readServerURL(); saved != "" {
		return saved
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
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf(
			"Error: SourceBridge server at %s requires authentication, but no token was provided (or the token is invalid).\n\n"+
				"       To fix this:\n"+
				"       1. Mint a token at %s/settings/tokens\n"+
				"       2. Re-run with --token=ca_... (saved to ~/.sourcebridge/token)\n"+
				"       Or export SOURCEBRIDGE_API_TOKEN in your shell.\n",
			serverURL, serverURL,
		)
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("this SourceBridge instance doesn't expose the agent_setup capability. Update or contact your admin")
	}
	if resp.StatusCode != http.StatusOK {
		rawBody, _ := io.ReadAll(resp.Body)
		return nil, &httpStatusError{status: resp.StatusCode, body: sanitizeStatusBody(string(rawBody))}
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
// Detection matches the stdio-proxy shape that MergeMCPJSON now writes:
//   - command is "sourcebridge" (or absolute path ending in /sourcebridge)
//     AND args[0] == "mcp-proxy" AND --server matches → UNCHANGED
//   - file absent → CREATE
//   - any other state (legacy HTTP shape, broken stdio, wrong URL, custom
//     command, broken JSON) → MODIFY
func dryRunMCPTag(mcpPath, serverURL string) string {
	data, err := os.ReadFile(mcpPath)
	if err != nil {
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
	// Proxy shape: command + args[0]=="mcp-proxy" + --server matches.
	cmd, _ := entry["command"].(string)
	if cmd == "sourcebridge" || strings.HasSuffix(cmd, "/sourcebridge") || strings.HasSuffix(cmd, "/sourcebridge.exe") {
		args, _ := entry["args"].([]interface{})
		if len(args) > 0 {
			first, _ := args[0].(string)
			if first == "mcp-proxy" {
				expectedServer := strings.TrimRight(serverURL, "/")
				for i, a := range args {
					s, _ := a.(string)
					if s == "--server" && i+1 < len(args) {
						v, _ := args[i+1].(string)
						if strings.TrimRight(v, "/") == expectedServer {
							return "UNCHANGED"
						}
					}
					if strings.HasPrefix(s, "--server=") {
						v := strings.TrimPrefix(s, "--server=")
						if strings.TrimRight(v, "/") == expectedServer {
							return "UNCHANGED"
						}
					}
				}
			}
		}
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
