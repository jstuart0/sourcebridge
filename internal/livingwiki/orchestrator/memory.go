// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
)

// MemoryPageStore is an in-memory implementation of [PageStore].
// It is intended for tests and local development. It does not persist across
// process restarts.
type MemoryPageStore struct {
	mu        sync.RWMutex
	canonical map[string]ast.Page  // key: repoID + "/" + pageID
	proposed  map[string]ast.Page  // key: repoID + "/" + prID + "/" + pageID
}

// NewMemoryPageStore returns an empty in-memory page store.
func NewMemoryPageStore() *MemoryPageStore {
	return &MemoryPageStore{
		canonical: make(map[string]ast.Page),
		proposed:  make(map[string]ast.Page),
	}
}

// Compile-time interface check.
var _ PageStore = (*MemoryPageStore)(nil)

func (m *MemoryPageStore) GetCanonical(_ context.Context, repoID, pageID string) (ast.Page, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.canonical[canonicalKey(repoID, pageID)]
	return p, ok, nil
}

func (m *MemoryPageStore) SetCanonical(_ context.Context, repoID string, page ast.Page) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.canonical[canonicalKey(repoID, page.ID)] = page
	return nil
}

func (m *MemoryPageStore) DeleteCanonical(_ context.Context, repoID, pageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.canonical, canonicalKey(repoID, pageID))
	return nil
}

func (m *MemoryPageStore) GetProposed(_ context.Context, repoID, prID, pageID string) (ast.Page, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.proposed[proposedKey(repoID, prID, pageID)]
	return p, ok, nil
}

func (m *MemoryPageStore) SetProposed(_ context.Context, repoID, prID string, page ast.Page) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proposed[proposedKey(repoID, prID, page.ID)] = page
	return nil
}

func (m *MemoryPageStore) ListProposed(_ context.Context, repoID, prID string) ([]ast.Page, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	prefix := repoID + "/" + prID + "/"
	var pages []ast.Page
	for k, p := range m.proposed {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			pages = append(pages, p)
		}
	}
	return pages, nil
}

func (m *MemoryPageStore) DeleteProposed(_ context.Context, repoID, prID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := repoID + "/" + prID + "/"
	for k := range m.proposed {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(m.proposed, k)
		}
	}
	return nil
}

func (m *MemoryPageStore) PromoteProposed(ctx context.Context, repoID, prID string) error {
	proposed, err := m.ListProposed(ctx, repoID, prID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range proposed {
		// Get the current canonical page (may not exist — that's fine for cold-start).
		canonical, _ := m.canonical[canonicalKey(repoID, p.ID)]
		promoted := ast.Promote(canonical, p)
		m.canonical[canonicalKey(repoID, promoted.ID)] = promoted
	}
	// Discard proposed pages after promotion.
	prefix := repoID + "/" + prID + "/"
	for k := range m.proposed {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(m.proposed, k)
		}
	}
	return nil
}

func canonicalKey(repoID, pageID string) string {
	return repoID + "/" + pageID
}

func proposedKey(repoID, prID, pageID string) string {
	return repoID + "/" + prID + "/" + pageID
}

// MemoryWikiPR is an in-memory implementation of [ExtendedWikiPR].
// It captures the files and state for test inspection.
type MemoryWikiPR struct {
	mu       sync.Mutex
	id       string
	branch   string
	title    string
	body     string
	files    map[string][]byte
	opened   bool
	merged   bool
	closed   bool
	commits  []Commit    // commits appended via AppendCommitToBranch
	comments []string    // comments posted via PostComment
}

// NewMemoryWikiPR returns a MemoryWikiPR with the given ID.
func NewMemoryWikiPR(id string) *MemoryWikiPR {
	return &MemoryWikiPR{id: id}
}

// Compile-time interface checks.
var _ WikiPR = (*MemoryWikiPR)(nil)
var _ ExtendedWikiPR = (*MemoryWikiPR)(nil)

func (m *MemoryWikiPR) ID() string { return m.id }

func (m *MemoryWikiPR) Open(_ context.Context, branch, title, body string, files map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.opened {
		return fmt.Errorf("MemoryWikiPR: PR %q is already open", m.id)
	}
	m.branch = branch
	m.title = title
	m.body = body
	m.files = make(map[string][]byte, len(files))
	for k, v := range files {
		cp := make([]byte, len(v))
		copy(cp, v)
		m.files[k] = cp
	}
	m.opened = true
	return nil
}

func (m *MemoryWikiPR) Merged(_ context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.merged, nil
}

func (m *MemoryWikiPR) Closed(_ context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed, nil
}

// SimulateMerge marks the PR as merged (for test control).
func (m *MemoryWikiPR) SimulateMerge() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.merged = true
}

// SimulateClose marks the PR as closed without merge (for test control).
func (m *MemoryWikiPR) SimulateClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
}

// IsOpen reports whether the PR has been opened.
func (m *MemoryWikiPR) IsOpen() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opened
}

// Branch returns the branch name passed to Open.
func (m *MemoryWikiPR) Branch() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.branch
}

// Title returns the PR title.
func (m *MemoryWikiPR) Title() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.title
}

// Files returns a copy of the files committed to this PR.
func (m *MemoryWikiPR) Files() map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]byte, len(m.files))
	for k, v := range m.files {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// AppendCommitToBranch records a new commit and merges its files into the PR's
// file set. It never force-pushes; each call appends one commit to the internal
// log.
func (m *MemoryWikiPR) AppendCommitToBranch(_ context.Context, branch string, files map[string][]byte, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = make(map[string][]byte)
	}
	// Merge files into the PR's current file set.
	filesCopy := make(map[string][]byte, len(files))
	for k, v := range files {
		cp := make([]byte, len(v))
		copy(cp, v)
		m.files[k] = cp
		filesCopy[k] = cp
	}
	m.commits = append(m.commits, Commit{
		SHA:            fmt.Sprintf("bot-%d", len(m.commits)+1),
		CommitterName:  SourceBridgeCommitterName,
		CommitterEmail: SourceBridgeCommitterEmail,
		Files:          filesCopy,
	})
	_ = branch
	_ = message
	return nil
}

// ListCommitsOnBranch returns commits recorded since the given time.
// Bot commits are those with CommitterName == SourceBridgeCommitterName.
func (m *MemoryWikiPR) ListCommitsOnBranch(_ context.Context, _ string, since time.Time) ([]Commit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Commit
	for _, c := range m.commits {
		// All memory commits have zero time, so return all when since is zero;
		// when since is set, include commits whose time is at or after since.
		// Since MemoryWikiPR does not store commit timestamps, we return all
		// commits regardless of since (they are effectively "all at now").
		_ = since
		result = append(result, c)
	}
	return result, nil
}

// PostComment appends a comment to the PR's comment log.
func (m *MemoryWikiPR) PostComment(_ context.Context, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.comments = append(m.comments, body)
	return nil
}

// UpdateDescription replaces the PR description body.
func (m *MemoryWikiPR) UpdateDescription(_ context.Context, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.body = body
	return nil
}

// AddHumanCommit adds a simulated human (non-bot) commit to the PR's commit
// log. Used by tests to simulate reviewer activity on the PR branch.
func (m *MemoryWikiPR) AddHumanCommit(sha string, files map[string][]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	filesCopy := make(map[string][]byte, len(files))
	for k, v := range files {
		cp := make([]byte, len(v))
		copy(cp, v)
		filesCopy[k] = cp
	}
	m.commits = append(m.commits, Commit{
		SHA:            sha,
		CommitterName:  "human-reviewer",
		CommitterEmail: "reviewer@example.com",
		Files:          filesCopy,
	})
}

// CommitCount returns the number of commits recorded on this PR.
func (m *MemoryWikiPR) CommitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.commits)
}

// Comments returns the list of comments posted to this PR.
func (m *MemoryWikiPR) Comments() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.comments))
	copy(out, m.comments)
	return out
}

// Body returns the current PR description body.
func (m *MemoryWikiPR) Body() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.body
}

// FilesystemRepoWriter is a [RepoWriter] that writes files to a local directory.
// The root directory is the base; file paths from the orchestrator are joined
// to this root. Suitable for tests and the FilesystemRepoWriter integration test.
type FilesystemRepoWriter struct {
	root string
}

// NewFilesystemRepoWriter creates a FilesystemRepoWriter that writes to root.
func NewFilesystemRepoWriter(root string) *FilesystemRepoWriter {
	return &FilesystemRepoWriter{root: root}
}

// Compile-time interface check.
var _ RepoWriter = (*FilesystemRepoWriter)(nil)

func (f *FilesystemRepoWriter) WriteFiles(_ context.Context, files map[string][]byte) error {
	// Import os and path/filepath locally to avoid cluttering the package-level imports.
	// We use the standard library via the helper below.
	return writeFilesToDir(f.root, files)
}

// MemoryExtendedRepoWriter is an in-memory implementation of [ExtendedRepoWriter].
// It is suitable for tests that need to verify commits written to a branch
// without a real git repository.
type MemoryExtendedRepoWriter struct {
	mu      sync.Mutex
	commits []Commit
	files   map[string][]byte
}

// NewMemoryExtendedRepoWriter creates an empty in-memory ExtendedRepoWriter.
func NewMemoryExtendedRepoWriter() *MemoryExtendedRepoWriter {
	return &MemoryExtendedRepoWriter{files: make(map[string][]byte)}
}

// Compile-time interface check.
var _ ExtendedRepoWriter = (*MemoryExtendedRepoWriter)(nil)

func (m *MemoryExtendedRepoWriter) WriteFiles(_ context.Context, files map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range files {
		cp := make([]byte, len(v))
		copy(cp, v)
		m.files[k] = cp
	}
	return nil
}

func (m *MemoryExtendedRepoWriter) AppendCommitToBranch(_ context.Context, branch string, files map[string][]byte, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	filesCopy := make(map[string][]byte, len(files))
	for k, v := range files {
		cp := make([]byte, len(v))
		copy(cp, v)
		m.files[k] = cp
		filesCopy[k] = cp
	}
	m.commits = append(m.commits, Commit{
		SHA:            fmt.Sprintf("writer-commit-%d", len(m.commits)+1),
		CommitterName:  SourceBridgeCommitterName,
		CommitterEmail: SourceBridgeCommitterEmail,
		Files:          filesCopy,
	})
	_ = branch
	_ = message
	return nil
}

func (m *MemoryExtendedRepoWriter) ListCommitsOnBranch(_ context.Context, _ string, _ time.Time) ([]Commit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Commit, len(m.commits))
	copy(result, m.commits)
	return result, nil
}

// CommitCount returns the number of commits written to this writer.
func (m *MemoryExtendedRepoWriter) CommitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.commits)
}

// Files returns a copy of all files written to this writer.
func (m *MemoryExtendedRepoWriter) Files() map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]byte, len(m.files))
	for k, v := range m.files {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}
