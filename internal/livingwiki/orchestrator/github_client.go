// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// github_client.go implements the ExtendedWikiPR and ExtendedRepoWriter ports
// via the GitHub REST API (no SDK; plain net/http).
//
// # Authentication
//
// All requests carry an "Authorization: Bearer <token>" header. The token may
// be a Personal Access Token or a GitHub App installation token; the client
// treats them identically.
//
// # Rate limiting
//
// GitHub's primary rate limit is expressed via X-RateLimit-Remaining and
// X-RateLimit-Reset (Unix timestamp). When Remaining reaches 0 the client
// sleeps until Reset, capped at 60 s. If Reset is more than 60 s away it
// returns an error rather than blocking indefinitely.
//
// # Retry policy
//
// 5xx and 429 responses are retried up to three times with exponential back-off
// (1 s, 2 s, 4 s). 4xx errors (excluding 429) are returned immediately.
//
// # Writing files via the Git Data API
//
// GitHub does not expose a "commit multiple files" endpoint; instead the client
// performs the four-step Git Data dance:
//
//  1. GET /repos/{o}/{r}/git/ref/heads/{branch} → base tree SHA.
//  2. POST /repos/{o}/{r}/git/blobs            → one blob per file.
//  3. POST /repos/{o}/{r}/git/trees            → new tree from blobs.
//  4. POST /repos/{o}/{r}/git/commits          → new commit (with bot committer).
//  5. PATCH /repos/{o}/{r}/git/refs/heads/{branch} → advance the ref.
//
// When the branch does not exist, the client creates it from the repo's default
// branch first, then performs steps 1–5.
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
	// githubAPIBaseURL is the default GitHub API base.
	githubAPIBaseURL = "https://api.github.com"

	// maxGitHubRetries is the maximum number of retries for 5xx / 429 responses.
	maxGitHubRetries = 3

	// maxGitHubRateLimitSleep is the maximum amount of time we'll sleep when a
	// rate-limit reset is in the future. If the reset is further away we return
	// an error.
	maxGitHubRateLimitSleep = 60 * time.Second
)

// GitHubAPIError is the typed error returned when the GitHub API responds with
// a non-2xx status. Callers can use [IsRateLimited] and [IsNotFound] to
// pattern-match.
type GitHubAPIError struct {
	// StatusCode is the HTTP status code.
	StatusCode int
	// Message is the human-readable message from GitHub's error body.
	Message string
}

func (e *GitHubAPIError) Error() string {
	return fmt.Sprintf("github API error %d: %s", e.StatusCode, e.Message)
}

// IsRateLimited reports whether err is a GitHub rate-limit error (429 or 403
// with a rate-limit message). Unwraps the error chain to find a [GitHubAPIError].
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	var ge *GitHubAPIError
	if errors.As(err, &ge) {
		return ge.StatusCode == http.StatusTooManyRequests ||
			(ge.StatusCode == http.StatusForbidden && strings.Contains(ge.Message, "rate limit"))
	}
	return false
}

// IsNotFound reports whether err is a GitHub 404 not-found error.
// Unwraps the error chain to find a [GitHubAPIError].
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var ge *GitHubAPIError
	return errors.As(err, &ge) && ge.StatusCode == http.StatusNotFound
}

func asGitHubError(err error, out **GitHubAPIError) bool {
	return errors.As(err, out)
}

// GitHubClientConfig holds construction parameters for [GitHubClient].
type GitHubClientConfig struct {
	// Owner is the GitHub repository owner (user or organisation).
	Owner string
	// Repo is the GitHub repository name.
	Repo string
	// Token is a PAT or GitHub App installation token.
	Token string
	// BaseURL is the API base (defaults to https://api.github.com).
	// Override for GitHub Enterprise Server.
	BaseURL string
	// DefaultBranch is the repo's main branch (defaults to "main").
	DefaultBranch string
	// HTTPTimeout is the per-request timeout (defaults to 30 s).
	HTTPTimeout time.Duration
}

func (c GitHubClientConfig) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return githubAPIBaseURL
}

func (c GitHubClientConfig) defaultBranch() string {
	if c.DefaultBranch != "" {
		return c.DefaultBranch
	}
	return "main"
}

func (c GitHubClientConfig) httpTimeout() time.Duration {
	if c.HTTPTimeout > 0 {
		return c.HTTPTimeout
	}
	return 30 * time.Second
}

// GitHubClient implements [ExtendedWikiPR] and [ExtendedRepoWriter] against
// the GitHub REST API using stdlib net/http. A single instance is bound to one
// PR (identified by a PR number that is assigned when [Open] is first called).
//
// Construct via [NewGitHubClient].
type GitHubClient struct {
	cfg            GitHubClientConfig
	http           *http.Client
	prNum          int           // populated by Open; 0 means not yet opened
	branch         string        // populated by Open
	retryBaseDelay time.Duration // base delay for exponential back-off; 0 means 1s
}

func (g *GitHubClient) retryDelay(attempt int) time.Duration {
	base := g.retryBaseDelay
	if base <= 0 {
		base = time.Second
	}
	return time.Duration(math.Pow(2, float64(attempt-1))) * base
}

// Compile-time interface checks.
var _ ExtendedWikiPR = (*GitHubClient)(nil)
var _ ExtendedRepoWriter = (*GitHubClient)(nil)

// NewGitHubClient constructs a [GitHubClient].
func NewGitHubClient(cfg GitHubClientConfig) *GitHubClient {
	return &GitHubClient{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.httpTimeout()},
	}
}

// ─── WikiPR ──────────────────────────────────────────────────────────────────

// ID returns a string form of the PR number, or "" before [Open] is called.
func (g *GitHubClient) ID() string {
	if g.prNum == 0 {
		return ""
	}
	return strconv.Itoa(g.prNum)
}

// Branch returns the PR's head branch name.
func (g *GitHubClient) Branch() string { return g.branch }

// Open creates the PR by committing files to branch, then calling the PR
// creation API. It is idempotent when the branch already exists (it appends a
// commit rather than failing).
//
// The WikiPR.Open contract: branch is the head branch name; files are committed
// there, then a PR from branch → default branch is opened.
func (g *GitHubClient) Open(ctx context.Context, branch, title, body string, files map[string][]byte) error {
	g.branch = branch

	// Commit the files to the branch (creates the branch if needed).
	if len(files) > 0 {
		commitMsg := fmt.Sprintf("wiki: initial generation (sourcebridge) on %s", branch)
		if err := g.appendCommit(ctx, branch, files, commitMsg); err != nil {
			return fmt.Errorf("github_client: Open: commit files: %w", err)
		}
	}

	// Create the pull request.
	type prPayload struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}
	payload := prPayload{
		Title: title,
		Body:  body,
		Head:  branch,
		Base:  g.cfg.defaultBranch(),
	}

	var resp struct {
		Number int `json:"number"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls", g.cfg.Owner, g.cfg.Repo)
	if err := g.do(ctx, http.MethodPost, path, payload, &resp); err != nil {
		return fmt.Errorf("github_client: create PR: %w", err)
	}
	g.prNum = resp.Number
	return nil
}

// Merged reports whether the PR has been merged.
func (g *GitHubClient) Merged(ctx context.Context) (bool, error) {
	state, err := g.prState(ctx)
	if err != nil {
		return false, err
	}
	return state.merged, nil
}

// Closed reports whether the PR was closed without merging.
func (g *GitHubClient) Closed(ctx context.Context) (bool, error) {
	state, err := g.prState(ctx)
	if err != nil {
		return false, err
	}
	return state.closed, nil
}

type githubPRState struct {
	merged bool
	closed bool
}

func (g *GitHubClient) prState(ctx context.Context) (githubPRState, error) {
	if g.prNum == 0 {
		return githubPRState{}, fmt.Errorf("github_client: PR not yet opened")
	}
	var resp struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", g.cfg.Owner, g.cfg.Repo, g.prNum)
	if err := g.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return githubPRState{}, fmt.Errorf("github_client: get PR state: %w", err)
	}
	return githubPRState{
		merged: resp.Merged,
		closed: resp.State == "closed" && !resp.Merged,
	}, nil
}

// PostComment posts a comment on the PR.
func (g *GitHubClient) PostComment(ctx context.Context, body string) error {
	if g.prNum == 0 {
		return fmt.Errorf("github_client: PR not yet opened")
	}
	payload := map[string]string{"body": body}
	// PR comments use the issues endpoint.
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", g.cfg.Owner, g.cfg.Repo, g.prNum)
	if err := g.do(ctx, http.MethodPost, path, payload, nil); err != nil {
		return fmt.Errorf("github_client: post comment: %w", err)
	}
	return nil
}

// UpdateDescription replaces the PR description.
func (g *GitHubClient) UpdateDescription(ctx context.Context, body string) error {
	if g.prNum == 0 {
		return fmt.Errorf("github_client: PR not yet opened")
	}
	payload := map[string]string{"body": body}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", g.cfg.Owner, g.cfg.Repo, g.prNum)
	if err := g.do(ctx, http.MethodPatch, path, payload, nil); err != nil {
		return fmt.Errorf("github_client: update PR description: %w", err)
	}
	return nil
}

// ─── ExtendedWikiPR ──────────────────────────────────────────────────────────

// AppendCommitToBranch writes files to branch as a new commit without
// force-pushing.
func (g *GitHubClient) AppendCommitToBranch(ctx context.Context, branch string, files map[string][]byte, message string) error {
	if err := g.appendCommit(ctx, branch, files, message); err != nil {
		return fmt.Errorf("github_client: AppendCommitToBranch: %w", err)
	}
	return nil
}

// ListCommitsOnBranch returns commits on branch at or after since, oldest-first.
func (g *GitHubClient) ListCommitsOnBranch(ctx context.Context, branch string, since time.Time) ([]Commit, error) {
	params := url.Values{}
	params.Set("sha", branch)
	if !since.IsZero() {
		params.Set("since", since.UTC().Format(time.RFC3339))
	}
	path := fmt.Sprintf("/repos/%s/%s/commits?%s", g.cfg.Owner, g.cfg.Repo, params.Encode())

	var raw []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := g.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, fmt.Errorf("github_client: list commits: %w", err)
	}

	commits := make([]Commit, len(raw))
	for i, r := range raw {
		commits[i] = Commit{
			SHA:            r.SHA,
			CommitterName:  r.Commit.Committer.Name,
			CommitterEmail: r.Commit.Committer.Email,
		}
	}
	// GitHub returns newest-first; reverse to oldest-first.
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// ─── ExtendedRepoWriter (non-PR branch writes) ───────────────────────────────

// WriteFiles writes files to the default branch.
func (g *GitHubClient) WriteFiles(ctx context.Context, files map[string][]byte) error {
	return g.appendCommit(ctx, g.cfg.defaultBranch(), files, "wiki: update (sourcebridge)")
}

// ─── Git Data API helpers ────────────────────────────────────────────────────

// appendCommit performs the full GitHub Git Data dance:
// ensure branch exists → get base SHA → create blobs → create tree → create commit → update ref.
func (g *GitHubClient) appendCommit(ctx context.Context, branch string, files map[string][]byte, message string) error {
	// 1. Ensure the branch exists (creates from defaultBranch if missing).
	baseSHA, err := g.ensureBranch(ctx, branch)
	if err != nil {
		return err
	}

	// 2. Create one blob per file.
	type treeEntry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	entries := make([]treeEntry, 0, len(files))
	for path, content := range files {
		blobSHA, blobErr := g.createBlob(ctx, content)
		if blobErr != nil {
			return fmt.Errorf("create blob for %q: %w", path, blobErr)
		}
		entries = append(entries, treeEntry{
			Path: path,
			Mode: "100644",
			Type: "blob",
			SHA:  blobSHA,
		})
	}

	// 3. Get the current commit's tree SHA.
	currentCommitSHA, treeSHA, err := g.resolveCommitTree(ctx, baseSHA)
	if err != nil {
		return err
	}

	// 4. Create a new tree.
	newTreeSHA, err := g.createTree(ctx, treeSHA, entries)
	if err != nil {
		return err
	}

	// 5. Create the commit.
	newCommitSHA, err := g.createCommit(ctx, message, newTreeSHA, currentCommitSHA)
	if err != nil {
		return err
	}

	// 6. Advance the branch ref.
	return g.updateRef(ctx, branch, newCommitSHA)
}

// ensureBranch returns the current HEAD SHA for branch, creating the branch
// from defaultBranch if it does not exist.
func (g *GitHubClient) ensureBranch(ctx context.Context, branch string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", g.cfg.Owner, g.cfg.Repo, branch)
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := g.do(ctx, http.MethodGet, path, nil, &ref)
	if err == nil {
		return ref.Object.SHA, nil
	}
	if !IsNotFound(err) {
		return "", fmt.Errorf("get branch ref %q: %w", branch, err)
	}

	// Branch does not exist — get the default branch SHA and create the branch.
	defaultPath := fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", g.cfg.Owner, g.cfg.Repo, g.cfg.defaultBranch())
	var defaultRef struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err2 := g.do(ctx, http.MethodGet, defaultPath, nil, &defaultRef); err2 != nil {
		return "", fmt.Errorf("get default branch ref: %w", err2)
	}
	baseSHA := defaultRef.Object.SHA

	// Create the new branch.
	createPayload := map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": baseSHA,
	}
	if err3 := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", g.cfg.Owner, g.cfg.Repo), createPayload, nil); err3 != nil {
		return "", fmt.Errorf("create branch %q: %w", branch, err3)
	}
	return baseSHA, nil
}

func (g *GitHubClient) createBlob(ctx context.Context, content []byte) (string, error) {
	payload := map[string]string{
		"content":  base64.StdEncoding.EncodeToString(content),
		"encoding": "base64",
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/git/blobs", g.cfg.Owner, g.cfg.Repo)
	if err := g.do(ctx, http.MethodPost, path, payload, &resp); err != nil {
		return "", err
	}
	return resp.SHA, nil
}

// resolveCommitTree resolves a commit SHA (or branch ref SHA) to the
// (commitSHA, treeSHA) pair. GitHub ref objects may point to a commit or a tag;
// we always expect a commit here.
func (g *GitHubClient) resolveCommitTree(ctx context.Context, commitSHA string) (string, string, error) {
	path := fmt.Sprintf("/repos/%s/%s/git/commits/%s", g.cfg.Owner, g.cfg.Repo, commitSHA)
	var resp struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", "", fmt.Errorf("get commit %s: %w", commitSHA, err)
	}
	return resp.SHA, resp.Tree.SHA, nil
}

func (g *GitHubClient) createTree(ctx context.Context, baseTreeSHA string, entries interface{}) (string, error) {
	payload := map[string]interface{}{
		"base_tree": baseTreeSHA,
		"tree":      entries,
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/git/trees", g.cfg.Owner, g.cfg.Repo)
	if err := g.do(ctx, http.MethodPost, path, payload, &resp); err != nil {
		return "", fmt.Errorf("create tree: %w", err)
	}
	return resp.SHA, nil
}

func (g *GitHubClient) createCommit(ctx context.Context, message, treeSHA, parentSHA string) (string, error) {
	type committerInfo struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	payload := map[string]interface{}{
		"message": message,
		"tree":    treeSHA,
		"parents": []string{parentSHA},
		"committer": committerInfo{
			Name:  SourceBridgeCommitterName,
			Email: SourceBridgeCommitterEmail,
		},
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/git/commits", g.cfg.Owner, g.cfg.Repo)
	if err := g.do(ctx, http.MethodPost, path, payload, &resp); err != nil {
		return "", fmt.Errorf("create commit: %w", err)
	}
	return resp.SHA, nil
}

func (g *GitHubClient) updateRef(ctx context.Context, branch, commitSHA string) error {
	payload := map[string]interface{}{
		"sha":   commitSHA,
		"force": false,
	}
	path := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", g.cfg.Owner, g.cfg.Repo, branch)
	if err := g.do(ctx, http.MethodPatch, path, payload, nil); err != nil {
		return fmt.Errorf("update ref %s: %w", branch, err)
	}
	return nil
}

// ─── HTTP core ───────────────────────────────────────────────────────────────

// do executes an API call with retry logic. method is the HTTP method; path is
// relative to the base URL (must start with "/"). reqBody is marshalled to JSON
// when non-nil. respBody is populated from the response JSON when non-nil.
func (g *GitHubClient) do(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	for attempt := 0; attempt <= maxGitHubRetries; attempt++ {
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

		// Retry on 429 and 5xx; return immediately on all other errors.
		var ge *GitHubAPIError
		if asGitHubError(err, &ge) {
			if ge.StatusCode == http.StatusTooManyRequests || ge.StatusCode >= 500 {
				if attempt < maxGitHubRetries {
					continue
				}
			}
		}
		return err
	}
	return fmt.Errorf("github_client: %s %s: exceeded retry limit", method, path)
}

// doOnce executes a single HTTP request against the GitHub API.
func (g *GitHubClient) doOnce(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	fullURL := g.cfg.baseURL() + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	// Rate-limit check before processing the body.
	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining == "0" {
		if resetHeader := resp.Header.Get("X-RateLimit-Reset"); resetHeader != "" {
			resetUnix, parseErr := strconv.ParseInt(resetHeader, 10, 64)
			if parseErr == nil {
				sleepUntil := time.Unix(resetUnix, 0)
				sleepFor := time.Until(sleepUntil)
				if sleepFor > maxGitHubRateLimitSleep {
					return &GitHubAPIError{
						StatusCode: resp.StatusCode,
						Message:    fmt.Sprintf("rate limit exhausted; reset in %s (exceeds %s cap)", sleepFor.Round(time.Second), maxGitHubRateLimitSleep),
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
		return fmt.Errorf("read response body: %w", readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Parse the GitHub error body.
		var errBody struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rawBody, &errBody)
		msg := errBody.Message
		if msg == "" {
			msg = string(rawBody)
		}
		return &GitHubAPIError{StatusCode: resp.StatusCode, Message: msg}
	}

	if respBody != nil && len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
