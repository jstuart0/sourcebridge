// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// gitlab_client.go implements the ExtendedWikiPR and ExtendedRepoWriter ports
// via the GitLab REST API (no SDK; plain net/http).
//
// # Authentication
//
// All requests carry a "PRIVATE-TOKEN: <token>" header. The token may be a
// personal access token or a project access token.
//
// # Rate limiting
//
// GitLab expresses rate limits via RateLimit-Remaining and RateLimit-Reset
// headers (same semantics as GitHub). The same cap and error logic applies.
//
// # Retry policy
//
// 5xx and 429 responses are retried up to three times with the same exponential
// back-off (1 s, 2 s, 4 s) used by the GitHub client.
//
// # Writing files
//
// GitLab's Repository Commits API accepts a multi-file commit in a single call:
//
//	POST /projects/{id}/repository/commits
//	{
//	  "branch":          "<branch>",
//	  "commit_message":  "<message>",
//	  "author_name":     "<name>",
//	  "author_email":    "<email>",
//	  "actions": [
//	    {"action": "create"|"update", "file_path": "<path>", "content": "<b64>", "encoding": "base64"},
//	    …
//	  ]
//	}
//
// This is cleaner than GitHub's four-step tree dance. The client uses "create"
// for new files and "update" for existing ones; when it cannot determine which,
// it sends "create" and falls back to "update" on a 400.
//
// Branch creation uses POST /projects/{id}/repository/branches.
package orchestrator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	gitlabAPIBaseURL         = "https://gitlab.com"
	maxGitLabRetries         = 3
	maxGitLabRateLimitSleep  = 60 * time.Second
)

// GitLabAPIError is the typed error returned by [GitLabClient] on non-2xx
// responses. Callers can use [IsGitLabRateLimited] and [IsGitLabNotFound].
type GitLabAPIError struct {
	StatusCode int
	Message    string
}

func (e *GitLabAPIError) Error() string {
	return fmt.Sprintf("gitlab API error %d: %s", e.StatusCode, e.Message)
}

// IsGitLabRateLimited reports whether err is a GitLab rate-limit error.
// Unwraps the error chain to find a [GitLabAPIError].
func IsGitLabRateLimited(err error) bool {
	var ge *GitLabAPIError
	return err != nil && errors.As(err, &ge) && ge.StatusCode == http.StatusTooManyRequests
}

// IsGitLabNotFound reports whether err is a GitLab 404 not-found error.
// Unwraps the error chain to find a [GitLabAPIError].
func IsGitLabNotFound(err error) bool {
	var ge *GitLabAPIError
	return err != nil && errors.As(err, &ge) && ge.StatusCode == http.StatusNotFound
}

// GitLabClientConfig holds construction parameters for [GitLabClient].
type GitLabClientConfig struct {
	// ProjectID is the GitLab project numeric ID or URL-encoded namespace/project.
	ProjectID string
	// Token is a personal or project access token.
	Token string
	// BaseURL is the API root (defaults to https://gitlab.com).
	// Override for self-managed GitLab instances.
	BaseURL string
	// DefaultBranch is the repo's main branch (defaults to "main").
	DefaultBranch string
	// HTTPTimeout is the per-request timeout (defaults to 30 s).
	HTTPTimeout time.Duration
}

func (c GitLabClientConfig) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return gitlabAPIBaseURL
}

func (c GitLabClientConfig) defaultBranch() string {
	if c.DefaultBranch != "" {
		return c.DefaultBranch
	}
	return "main"
}

func (c GitLabClientConfig) httpTimeout() time.Duration {
	if c.HTTPTimeout > 0 {
		return c.HTTPTimeout
	}
	return 30 * time.Second
}

// GitLabClient implements [ExtendedWikiPR] and [ExtendedRepoWriter] against
// the GitLab REST API.
//
// Construct via [NewGitLabClient].
type GitLabClient struct {
	cfg            GitLabClientConfig
	http           *http.Client
	mrIID          int           // internal ID assigned by GitLab on MR creation; 0 until Open
	branch         string        // populated by Open
	retryBaseDelay time.Duration // base delay for back-off; 0 means 1s
}

func (g *GitLabClient) retryDelay(attempt int) time.Duration {
	base := g.retryBaseDelay
	if base <= 0 {
		base = time.Second
	}
	return time.Duration(math.Pow(2, float64(attempt-1))) * base
}

// Compile-time interface checks.
var _ ExtendedWikiPR = (*GitLabClient)(nil)
var _ ExtendedRepoWriter = (*GitLabClient)(nil)

// NewGitLabClient constructs a [GitLabClient].
func NewGitLabClient(cfg GitLabClientConfig) *GitLabClient {
	return &GitLabClient{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.httpTimeout()},
	}
}

// encodedProjectID URL-encodes the project ID for use in path segments.
// Numeric IDs pass through unchanged; namespace/project paths are encoded.
func (g *GitLabClient) encodedProjectID() string {
	// If already a number, no encoding needed.
	if _, err := strconv.Atoi(g.cfg.ProjectID); err == nil {
		return g.cfg.ProjectID
	}
	return url.PathEscape(g.cfg.ProjectID)
}

// ─── WikiPR ──────────────────────────────────────────────────────────────────

// ID returns a string form of the MR IID, or "" before [Open] is called.
func (g *GitLabClient) ID() string {
	if g.mrIID == 0 {
		return ""
	}
	return strconv.Itoa(g.mrIID)
}

// Branch returns the MR's source branch name.
func (g *GitLabClient) Branch() string { return g.branch }

// Open commits files to branch and creates a GitLab merge request.
func (g *GitLabClient) Open(ctx context.Context, branch, title, body string, files map[string][]byte) error {
	g.branch = branch

	if len(files) > 0 {
		msg := fmt.Sprintf("wiki: initial generation (sourcebridge) on %s", branch)
		if err := g.appendCommit(ctx, branch, files, msg); err != nil {
			return fmt.Errorf("gitlab_client: Open: commit files: %w", err)
		}
	}

	type mrPayload struct {
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		Title        string `json:"title"`
		Description  string `json:"description"`
	}
	payload := mrPayload{
		SourceBranch: branch,
		TargetBranch: g.cfg.defaultBranch(),
		Title:        title,
		Description:  body,
	}

	var resp struct {
		IID int `json:"iid"`
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests", g.encodedProjectID())
	if err := g.do(ctx, http.MethodPost, path, payload, &resp); err != nil {
		return fmt.Errorf("gitlab_client: create MR: %w", err)
	}
	g.mrIID = resp.IID
	return nil
}

// Merged reports whether the MR state is "merged".
func (g *GitLabClient) Merged(ctx context.Context) (bool, error) {
	state, err := g.mrState(ctx)
	if err != nil {
		return false, err
	}
	return state == "merged", nil
}

// Closed reports whether the MR was closed without merging.
func (g *GitLabClient) Closed(ctx context.Context) (bool, error) {
	state, err := g.mrState(ctx)
	if err != nil {
		return false, err
	}
	return state == "closed", nil
}

func (g *GitLabClient) mrState(ctx context.Context) (string, error) {
	if g.mrIID == 0 {
		return "", fmt.Errorf("gitlab_client: MR not yet opened")
	}
	var resp struct {
		State string `json:"state"`
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", g.encodedProjectID(), g.mrIID)
	if err := g.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", fmt.Errorf("gitlab_client: get MR state: %w", err)
	}
	return resp.State, nil
}

// PostComment posts a note on the MR.
func (g *GitLabClient) PostComment(ctx context.Context, body string) error {
	if g.mrIID == 0 {
		return fmt.Errorf("gitlab_client: MR not yet opened")
	}
	payload := map[string]string{"body": body}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/notes", g.encodedProjectID(), g.mrIID)
	if err := g.do(ctx, http.MethodPost, path, payload, nil); err != nil {
		return fmt.Errorf("gitlab_client: post comment: %w", err)
	}
	return nil
}

// UpdateDescription replaces the MR description.
func (g *GitLabClient) UpdateDescription(ctx context.Context, body string) error {
	if g.mrIID == 0 {
		return fmt.Errorf("gitlab_client: MR not yet opened")
	}
	payload := map[string]string{"description": body}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", g.encodedProjectID(), g.mrIID)
	if err := g.do(ctx, http.MethodPut, path, payload, nil); err != nil {
		return fmt.Errorf("gitlab_client: update MR description: %w", err)
	}
	return nil
}

// ─── ExtendedWikiPR ──────────────────────────────────────────────────────────

// AppendCommitToBranch writes files to branch as a new commit.
func (g *GitLabClient) AppendCommitToBranch(ctx context.Context, branch string, files map[string][]byte, message string) error {
	if err := g.appendCommit(ctx, branch, files, message); err != nil {
		return fmt.Errorf("gitlab_client: AppendCommitToBranch: %w", err)
	}
	return nil
}

// ListCommitsOnBranch returns commits on branch at or after since, oldest-first.
func (g *GitLabClient) ListCommitsOnBranch(ctx context.Context, branch string, since time.Time) ([]Commit, error) {
	params := url.Values{}
	params.Set("ref_name", branch)
	if !since.IsZero() {
		params.Set("since", since.UTC().Format(time.RFC3339))
	}
	path := fmt.Sprintf("/api/v4/projects/%s/repository/commits?%s", g.encodedProjectID(), params.Encode())

	var raw []struct {
		ID          string `json:"id"`
		CommitterName  string `json:"committer_name"`
		CommitterEmail string `json:"committer_email"`
	}
	if err := g.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, fmt.Errorf("gitlab_client: list commits: %w", err)
	}

	commits := make([]Commit, len(raw))
	for i, r := range raw {
		commits[i] = Commit{
			SHA:            r.ID,
			CommitterName:  r.CommitterName,
			CommitterEmail: r.CommitterEmail,
		}
	}
	// GitLab returns newest-first; reverse to oldest-first.
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// ─── ExtendedRepoWriter (non-MR branch writes) ───────────────────────────────

// WriteFiles writes files to the default branch.
func (g *GitLabClient) WriteFiles(ctx context.Context, files map[string][]byte) error {
	return g.appendCommit(ctx, g.cfg.defaultBranch(), files, "wiki: update (sourcebridge)")
}

// ─── Repository Commits API helper ───────────────────────────────────────────

// appendCommit commits files to a branch using GitLab's Repository Commits API.
// It creates the branch if it does not exist.
func (g *GitLabClient) appendCommit(ctx context.Context, branch string, files map[string][]byte, message string) error {
	// Ensure the branch exists first.
	if err := g.ensureBranch(ctx, branch); err != nil {
		return err
	}

	type action struct {
		Action   string `json:"action"`
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}

	actions := make([]action, 0, len(files))
	for path, content := range files {
		actions = append(actions, action{
			Action:   "create",
			FilePath: path,
			Content:  base64.StdEncoding.EncodeToString(content),
			Encoding: "base64",
		})
	}

	type commitPayload struct {
		Branch        string   `json:"branch"`
		CommitMessage string   `json:"commit_message"`
		AuthorName    string   `json:"author_name"`
		AuthorEmail   string   `json:"author_email"`
		Actions       []action `json:"actions"`
	}
	payload := commitPayload{
		Branch:        branch,
		CommitMessage: message,
		AuthorName:    SourceBridgeCommitterName,
		AuthorEmail:   SourceBridgeCommitterEmail,
		Actions:       actions,
	}

	path := fmt.Sprintf("/api/v4/projects/%s/repository/commits", g.encodedProjectID())
	err := g.do(ctx, http.MethodPost, path, payload, nil)
	if err == nil {
		return nil
	}

	// GitLab returns 400 if any file already exists with action="create".
	// Retry with action="update" for all files.
	var ge *GitLabAPIError
	if errors.As(err, &ge) && ge.StatusCode == http.StatusBadRequest {
		for i := range actions {
			actions[i].Action = "update"
		}
		payload.Actions = actions
		if retryErr := g.do(ctx, http.MethodPost, path, payload, nil); retryErr != nil {
			return fmt.Errorf("commit (update) to branch %q: %w", branch, retryErr)
		}
		return nil
	}
	return fmt.Errorf("commit to branch %q: %w", branch, err)
}

// ensureBranch creates branch from the default branch when it does not exist.
func (g *GitLabClient) ensureBranch(ctx context.Context, branch string) error {
	checkPath := fmt.Sprintf("/api/v4/projects/%s/repository/branches/%s",
		g.encodedProjectID(), url.PathEscape(branch))
	var dummy interface{}
	err := g.do(ctx, http.MethodGet, checkPath, nil, &dummy)
	if err == nil {
		return nil // branch exists
	}
	if !IsGitLabNotFound(err) {
		return fmt.Errorf("check branch %q: %w", branch, err)
	}

	// Create the branch from the default branch.
	payload := map[string]string{
		"branch": branch,
		"ref":    g.cfg.defaultBranch(),
	}
	createPath := fmt.Sprintf("/api/v4/projects/%s/repository/branches", g.encodedProjectID())
	if err2 := g.do(ctx, http.MethodPost, createPath, payload, nil); err2 != nil {
		return fmt.Errorf("create branch %q: %w", branch, err2)
	}
	return nil
}

// ─── HTTP core ───────────────────────────────────────────────────────────────

func (g *GitLabClient) do(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	for attempt := 0; attempt <= maxGitLabRetries; attempt++ {
		if attempt > 0 {
			sleep := g.retryDelay(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}

		err := g.doOnce(ctx, method, path, reqBody, respBody)
		if err == nil {
			return nil
		}

		var ge *GitLabAPIError
		if errors.As(err, &ge) {
			if ge.StatusCode == http.StatusTooManyRequests || ge.StatusCode >= 500 {
				if attempt < maxGitLabRetries {
					continue
				}
			}
		}
		return err
	}
	return fmt.Errorf("gitlab_client: %s %s: exceeded retry limit", method, path)
}

func (g *GitLabClient) doOnce(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	fullURL := g.cfg.baseURL() + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.cfg.Token)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	// Rate-limit handling.
	if remaining := resp.Header.Get("RateLimit-Remaining"); remaining == "0" {
		if resetHeader := resp.Header.Get("RateLimit-Reset"); resetHeader != "" {
			resetUnix, parseErr := strconv.ParseInt(resetHeader, 10, 64)
			if parseErr == nil {
				sleepFor := time.Until(time.Unix(resetUnix, 0))
				if sleepFor > maxGitLabRateLimitSleep {
					return &GitLabAPIError{
						StatusCode: resp.StatusCode,
						Message:    fmt.Sprintf("rate limit exhausted; reset in %s (exceeds %s cap)", sleepFor.Round(time.Second), maxGitLabRateLimitSleep),
					}
				}
				if sleepFor > 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(sleepFor):
					}
				}
			}
		}
	}

	rawBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("read response: %w", readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// GitLab errors may appear as {"message": "..."} or {"error": "..."}.
		var errBody struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(rawBody, &errBody)
		msg := errBody.Message
		if msg == "" {
			msg = errBody.Error
		}
		if msg == "" {
			msg = string(rawBody)
		}
		return &GitLabAPIError{StatusCode: resp.StatusCode, Message: msg}
	}

	if respBody != nil && len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
