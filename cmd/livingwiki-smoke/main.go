// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// livingwiki-smoke is the Tier-2 real-Confluence smoke binary for the
// living-wiki feature. It is intended to run as a Kubernetes CronJob on-thor,
// weekly, against a designated test Confluence space.
//
// # What it tests
//
//  1. Calls the SourceBridge GraphQL API to enable living-wiki for a designated
//     test repository via enableLivingWikiForRepo.
//  2. Polls the activity feed until the job reaches a terminal state.
//  3. Asserts pagesGenerated > 0 and status != "failed".
//  4. Reports success or failure to a Slack webhook (optional) and exits with
//     a non-zero code on failure so the CronJob reports it to the cluster.
//
// # Configuration
//
// All configuration is via environment variables. The CronJob template
// (deploy/kubernetes/base/cronjobs/livingwiki-smoke.yaml.example) shows how
// to wire the Kubernetes Secret values into the pod.
//
//	SOURCEBRIDGE_URL          SourceBridge instance base URL (required)
//	SOURCEBRIDGE_ADMIN_TOKEN  Bearer token for the admin API (required)
//	SMOKE_REPO_ID             Repository ID to enable/test living-wiki for (required)
//	SMOKE_SINK_KIND           Sink kind: CONFLUENCE | GIT_REPO | NOTION (default: CONFLUENCE)
//	SMOKE_SINK_INTEGRATION    Integration name for the sink (default: smoke-test)
//	SMOKE_POLL_TIMEOUT        How long to wait for the job to finish (default: 10m)
//	SMOKE_POLL_INTERVAL       How often to poll the activity feed (default: 5s)
//	SLACK_WEBHOOK_URL         Optional. If set, result is posted as a message.
//
// Exit codes:
//
//	0  smoke passed
//	1  smoke failed (job error, timeout, assertion failure, config error)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Configuration
// ─────────────────────────────────────────────────────────────────────────────

type config struct {
	SourceBridgeURL   string
	AdminToken        string
	RepoID            string
	SinkKind          string
	SinkIntegration   string
	PollTimeout       time.Duration
	PollInterval      time.Duration
	SlackWebhookURL   string
}

func loadConfig() (config, error) {
	cfg := config{
		SourceBridgeURL: os.Getenv("SOURCEBRIDGE_URL"),
		AdminToken:      os.Getenv("SOURCEBRIDGE_ADMIN_TOKEN"),
		RepoID:          os.Getenv("SMOKE_REPO_ID"),
		SinkKind:        envOrDefault("SMOKE_SINK_KIND", "CONFLUENCE"),
		SinkIntegration: envOrDefault("SMOKE_SINK_INTEGRATION", "smoke-test"),
		SlackWebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
	}

	if cfg.SourceBridgeURL == "" {
		return config{}, fmt.Errorf("SOURCEBRIDGE_URL is required")
	}
	if cfg.AdminToken == "" {
		return config{}, fmt.Errorf("SOURCEBRIDGE_ADMIN_TOKEN is required")
	}
	if cfg.RepoID == "" {
		return config{}, fmt.Errorf("SMOKE_REPO_ID is required")
	}

	var err error
	if s := os.Getenv("SMOKE_POLL_TIMEOUT"); s != "" {
		cfg.PollTimeout, err = time.ParseDuration(s)
		if err != nil {
			return config{}, fmt.Errorf("SMOKE_POLL_TIMEOUT: %w", err)
		}
	} else {
		cfg.PollTimeout = 10 * time.Minute
	}

	if s := os.Getenv("SMOKE_POLL_INTERVAL"); s != "" {
		cfg.PollInterval, err = time.ParseDuration(s)
		if err != nil {
			return config{}, fmt.Errorf("SMOKE_POLL_INTERVAL: %w", err)
		}
	} else {
		cfg.PollInterval = 5 * time.Second
	}

	cfg.SourceBridgeURL = strings.TrimRight(cfg.SourceBridgeURL, "/")
	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ─────────────────────────────────────────────────────────────────────────────
// GraphQL client
// ─────────────────────────────────────────────────────────────────────────────

type gqlClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newGQLClient(baseURL, token string) *gqlClient {
	return &gqlClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

func (c *gqlClient) do(ctx context.Context, req gqlRequest) (map[string]any, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(buf))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if errs, ok := result["errors"]; ok {
		b, _ := json.Marshal(errs)
		return nil, fmt.Errorf("graphql errors: %s", string(b))
	}

	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Smoke steps
// ─────────────────────────────────────────────────────────────────────────────

// enableLivingWiki calls enableLivingWikiForRepo and returns the jobId.
// Returns ("", nil) when the server accepted the mutation but the job was not
// started (kill-switch or globally-disabled case).
func enableLivingWiki(ctx context.Context, c *gqlClient, cfg config) (string, string, error) {
	mutation := `
		mutation EnableLivingWiki($input: EnableLivingWikiForRepoInput!) {
			enableLivingWikiForRepo(input: $input) {
				jobId
				notice
				settings {
					enabled
				}
			}
		}`

	resp, err := c.do(ctx, gqlRequest{
		Query: mutation,
		Variables: map[string]any{
			"input": map[string]any{
				"repositoryId": cfg.RepoID,
				"mode":         "DIRECT_PUBLISH",
				"sinks": []map[string]any{
					{
						"kind":            cfg.SinkKind,
						"integrationName": cfg.SinkIntegration,
						"audience":        "ENGINEER",
					},
				},
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("enableLivingWikiForRepo: %w", err)
	}

	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return "", "", fmt.Errorf("unexpected response shape: %v", resp)
	}
	result, _ := data["enableLivingWikiForRepo"].(map[string]any)
	if result == nil {
		return "", "", fmt.Errorf("missing enableLivingWikiForRepo in response: %v", data)
	}

	jobID, _ := result["jobId"].(string)
	notice, _ := result["notice"].(string)
	return jobID, notice, nil
}

// jobResult polls the activity feed until the living-wiki job for cfg.RepoID
// reaches a terminal state, then returns the pagesGenerated count and status.
func pollJobResult(ctx context.Context, c *gqlClient, cfg config, jobID string) (int, string, error) {
	deadline := time.Now().Add(cfg.PollTimeout)

	for {
		if time.Now().After(deadline) {
			return 0, "", fmt.Errorf("timed out waiting for job %q after %v", jobID, cfg.PollTimeout)
		}

		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(cfg.PollInterval):
		}

		query := `
			query ActivityFeed($repoID: ID!) {
				llmJobs(repositoryId: $repoID, type: "living_wiki_cold_start") {
					nodes {
						id
						status
						progress
						progressMessage
					}
				}
			}`

		resp, err := c.do(ctx, gqlRequest{
			Query:     query,
			Variables: map[string]any{"repoID": cfg.RepoID},
		})
		if err != nil {
			slog.Warn("poll: query error (will retry)", "err", err)
			continue
		}

		data, _ := resp["data"].(map[string]any)
		jobs, _ := data["llmJobs"].(map[string]any)
		nodes, _ := jobs["nodes"].([]any)

		for _, n := range nodes {
			node, _ := n.(map[string]any)
			id, _ := node["id"].(string)
			status, _ := node["status"].(string)
			msg, _ := node["progressMessage"].(string)

			if jobID != "" && id != jobID {
				continue
			}

			slog.Info("poll: job update", "id", id, "status", status, "msg", msg)

			switch status {
			case "done", "failed", "done_partial":
				pagesGenerated := 0
				if msg != "" {
					// progressMessage format: "Generated N/M pages" — extract N.
					var n2, m int
					fmt.Sscanf(msg, "Generated %d/%d", &n2, &m)
					pagesGenerated = n2
				}
				return pagesGenerated, status, nil
			}
		}
	}
}

// assertJobResult checks that the smoke run meets acceptance criteria.
func assertJobResult(pagesGenerated int, status string) error {
	if status == "failed" {
		return fmt.Errorf("job ended with status=failed")
	}
	if pagesGenerated <= 0 {
		return fmt.Errorf("expected pagesGenerated > 0, got %d (status=%s)", pagesGenerated, status)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Slack notification
// ─────────────────────────────────────────────────────────────────────────────

func notifySlack(webhookURL, message string) error {
	payload := map[string]string{"text": message}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	slog.Info("livingwiki-smoke: starting",
		"url", cfg.SourceBridgeURL,
		"repo", cfg.RepoID,
		"sink", cfg.SinkKind,
		"timeout", cfg.PollTimeout.String(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.PollTimeout+30*time.Second)
	defer cancel()

	client := newGQLClient(cfg.SourceBridgeURL, cfg.AdminToken)

	// Step 1: Enable living-wiki for the test repo.
	jobID, notice, err := enableLivingWiki(ctx, client, cfg)
	if err != nil {
		slog.Error("smoke: enableLivingWiki failed", "err", err)
		reportResult(cfg, false, fmt.Sprintf("enableLivingWikiForRepo failed: %v", err))
		os.Exit(1)
	}
	if notice != "" {
		slog.Warn("smoke: feature not started (kill-switch or globally disabled)", "notice", notice)
		reportResult(cfg, false, "living-wiki is disabled server-side: "+notice)
		os.Exit(1)
	}
	if jobID == "" {
		slog.Error("smoke: mutation returned empty jobId with no notice — unexpected state")
		reportResult(cfg, false, "mutation returned no jobId and no notice")
		os.Exit(1)
	}

	slog.Info("smoke: job enqueued", "job_id", jobID)

	// Step 2: Poll until terminal.
	pagesGenerated, status, err := pollJobResult(ctx, client, cfg, jobID)
	if err != nil {
		slog.Error("smoke: polling failed", "err", err)
		reportResult(cfg, false, fmt.Sprintf("poll failed: %v", err))
		os.Exit(1)
	}

	slog.Info("smoke: job terminal", "job_id", jobID, "status", status, "pages_generated", pagesGenerated)

	// Step 3: Assert.
	if err := assertJobResult(pagesGenerated, status); err != nil {
		slog.Error("smoke: assertion failed", "err", err)
		reportResult(cfg, false, err.Error())
		os.Exit(1)
	}

	msg := fmt.Sprintf("living-wiki smoke PASSED: %s pages_generated=%s status=%s repo=%s",
		cfg.SourceBridgeURL, strconv.Itoa(pagesGenerated), status, cfg.RepoID)
	slog.Info(msg)
	reportResult(cfg, true, msg)
}

func reportResult(cfg config, passed bool, msg string) {
	if cfg.SlackWebhookURL == "" {
		return
	}
	prefix := ":white_check_mark:"
	if !passed {
		prefix = ":x:"
	}
	_ = notifySlack(cfg.SlackWebhookURL, prefix+" *SourceBridge living-wiki smoke*: "+msg)
}
