// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sourcebridge/sourcebridge/internal/git"
)

// Watcher is the in-process passive change connector. It watches the
// working trees of indexed repositories via fsnotify, debounces raw
// kernel events into per-repo batches, filters via git.IsIgnoredPath,
// stamps a ChangeEvent, and submits to the Router.
//
// The watcher is goroutine-safe; one Watcher instance can serve every
// indexed repo in the process. A single Run goroutine drains the
// fsnotify event channel; per-repo debounce timers fire in their own
// goroutines.
//
// Lifecycle:
//
//	w, err := NewWatcher(router)
//	if err != nil { ... }
//	defer w.Close()
//	if err := w.Watch(repoID, repoPath); err != nil { ... }
//	go w.Run(ctx)
//
// Run blocks until ctx is cancelled or fsnotify's underlying watcher
// closes. Close is safe to call multiple times.
//
// Multi-tenant isolation: callers add only the repos a tenant can
// access. The watcher itself does not inspect tenant boundaries — it
// trusts its caller (the server-assembly site) to honor the existing
// MCPPermissionChecker pattern. Per-event re-checking would require
// a session, which the watcher does not have. The closed-loop design
// ensures repo-keyed isolation: an event for repo X can never affect
// repo Y's index data, so cross-tenant leak is impossible by
// construction once the per-repo watch list is gated correctly.
type Watcher struct {
	router *Router

	mu       sync.Mutex
	fsw      *fsnotify.Watcher
	repos    map[string]*watchedRepo // repoID → metadata
	pathToID map[string]string       // absolute repo path → repoID for quick reverse lookup

	debounce time.Duration
	now      func() time.Time

	// closed records whether Close has been called. Once true, Watch
	// rejects new repos and Run returns.
	closed bool
}

// watchedRepo holds the per-repo state the watcher tracks: the indexed
// repo path, its source.kind branding, and the in-flight debounce
// batch.
type watchedRepo struct {
	repoID   string
	repoPath string

	// pending accumulates raw fsnotify-derived path entries (repo-relative,
	// forward-slash) since the last debounce flush. Map for dedup so two
	// rapid CREATE+WRITE events for the same path collapse cheaply.
	pending map[string]FileChangeStatus

	// timer fires after debounce. Reset on every event.
	timer *time.Timer
}

// NewWatcher constructs a Watcher around an fsnotify.Watcher. Returns
// an error when fsnotify cannot allocate (typically inotify limits on
// Linux; documented for the operator runbook).
//
// debounce is the per-repo coalesce window — kernel events that arrive
// within this window for the same repo are batched into a single
// ChangeEvent. Phase 1.C wires the Balanced default (2s) at the Server
// assembly site; Phase 4 makes this per-repo configurable via the
// repository-mode column. Pass 0 to fall back to the 2s default.
func NewWatcher(router *Router, debounce time.Duration) (*Watcher, error) {
	if router == nil {
		return nil, errors.New("changewatch.Watcher: router is required")
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("changewatch.Watcher: fsnotify.NewWatcher: %w", err)
	}
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	return &Watcher{
		router:   router,
		fsw:      fsw,
		repos:    make(map[string]*watchedRepo),
		pathToID: make(map[string]string),
		debounce: debounce,
		now:      time.Now,
	}, nil
}

// SetNow overrides the time source for tests.
func (w *Watcher) SetNow(now func() time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = now
}

// Watch registers a repository for fsnotify monitoring. Adds the repo
// root and recurses into every subdirectory that survives the ignore
// filter (so node_modules / .git / vendor are skipped).
//
// Returns an error when fsnotify cannot add the path (typically
// inotify per-process limits on Linux). The operator runbook
// documents how to raise the limit.
//
// We resolve symlinks on the input path before storing it. fsnotify
// reports events with the filesystem's *real* path, not the symlinked
// path the caller passed in (most visibly on macOS where /var is a
// symlink to /private/var, but also any operator-mounted symlink in
// the repo path). Without EvalSymlinks the prefix-match in classify
// would fail to associate events with their repo, which surfaced as a
// 1.C bug discovered during the integration test write-up.
func (w *Watcher) Watch(repoID, repoPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("changewatch.Watcher: closed")
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolving repo path: %w", err)
	}
	// Resolve symlinks so the stored repoPath matches the path fsnotify
	// reports in event names. EvalSymlinks errors on missing paths;
	// surface them so a caller wiring up a deleted-then-recreated repo
	// gets an actionable error instead of mysterious silence.
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = resolved
	} else {
		return fmt.Errorf("resolving symlinks on repo path: %w", evalErr)
	}
	if _, exists := w.repos[repoID]; exists {
		return fmt.Errorf("changewatch.Watcher: repo %q already watched", repoID)
	}
	w.repos[repoID] = &watchedRepo{
		repoID:   repoID,
		repoPath: abs,
		pending:  make(map[string]FileChangeStatus),
	}
	w.pathToID[abs] = repoID

	// Recursively add directories. We collect errors but proceed where
	// possible — a single hidden subdir that can't be added shouldn't
	// disable the whole watch.
	var addErrs []error
	walkErr := filepath.Walk(abs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable subtrees
		}
		if !info.IsDir() {
			return nil
		}
		// Compute repo-relative path for the ignore decision.
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// Use IsIgnoredDir (component-name + hidden rules) here; the
		// IsIgnoredPath unknown-language rule is for files only and
		// would prune every legitimate package directory whose name
		// happens not to end in ".go"/".py"/etc.
		if rel != "." && git.IsIgnoredDir(abs, rel) {
			return filepath.SkipDir
		}
		if err := w.fsw.Add(path); err != nil {
			addErrs = append(addErrs, fmt.Errorf("add %s: %w", path, err))
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("walking repo: %w", walkErr)
	}
	if len(addErrs) > 0 {
		// Log but don't fail the whole watch — partial coverage is
		// better than no coverage. Surface the first error so the
		// caller can decide what to do.
		slog.Warn("changewatch.Watcher: partial directory coverage",
			"repo_id", repoID,
			"add_errors", len(addErrs),
			"first_err", addErrs[0],
		)
	}
	return nil
}

// Unwatch stops watching a repo. Idempotent.
func (w *Watcher) Unwatch(repoID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	repo := w.repos[repoID]
	if repo == nil {
		return
	}
	if repo.timer != nil {
		repo.timer.Stop()
	}
	// fsnotify.Remove is per-path; we walk the repo subtree to remove
	// every directory we added in Watch. Walk errors (permission
	// changes, races with deletion) are best-effort during teardown
	// and intentionally not returned: we still want to drop the repo
	// from our tracking maps below.
	_ = filepath.Walk(repo.repoPath, func(path string, info os.FileInfo, _ error) error {
		if info != nil && info.IsDir() {
			_ = w.fsw.Remove(path)
		}
		return nil
	})
	delete(w.repos, repoID)
	delete(w.pathToID, repo.repoPath)
}

// Run blocks while it drains fsnotify events. Returns when ctx is
// cancelled, when the underlying fsnotify watcher closes, or when
// Close is called.
//
// Errors on the fsnotify error channel are logged. A "events lost"
// condition (fsnotify dropping kernel events under load) is
// surfaced via slog.Error so the operator runbook's "investigate
// dropped events" guidance kicks in.
func (w *Watcher) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handleEvent(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			// fsnotify on Linux can drop events under load. We surface
			// this loud so the operator notices; the plan calls for a
			// special fsnotify_events_lost event in a future phase.
			slog.Error("changewatch.Watcher: fsnotify error", "err", err)
		}
	}
}

// Close stops the watcher and releases resources. Idempotent.
func (w *Watcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	for _, repo := range w.repos {
		if repo.timer != nil {
			repo.timer.Stop()
		}
	}
	return w.fsw.Close()
}

// handleEvent classifies a single fsnotify event and folds it into the
// matching repo's pending batch.
func (w *Watcher) handleEvent(ev fsnotify.Event) {
	repoID, repoPath, relPath := w.classify(ev.Name)
	if repoID == "" {
		return // event outside any watched repo, or filtered
	}
	if relPath == "" {
		return
	}

	// Special-case CREATE events on directories: a newly-created
	// directory must be added to the fsnotify watch list before we can
	// see further events under it. We stat early so the IsIgnoredPath
	// filter (which is file-shaped) doesn't reject the dir on its name
	// alone.
	status := classifyOp(ev.Op)
	if status == FileChangeAdded {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if !git.IsIgnoredDir(repoPath, relPath) {
				w.mu.Lock()
				_ = w.fsw.Add(ev.Name)
				w.mu.Unlock()
			}
			return // directory creation isn't itself a delta
		}
	}

	// File-shaped filter: ignored paths (node_modules / .git / vendor /
	// hidden / unknown-language).
	if git.IsIgnoredPath(repoPath, relPath) {
		return
	}
	if status == "" {
		return // CHMOD-only / unrecognized op
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	repo := w.repos[repoID]
	if repo == nil {
		return
	}

	// Coalesce: delete-after-create within the same batch becomes a
	// modify (the file was rewritten); add-after-delete stays an add.
	// The router's MergeIndexResult treats added/modified identically;
	// the distinction matters for the impact report.
	prev, exists := repo.pending[relPath]
	if !exists {
		repo.pending[relPath] = status
	} else {
		switch {
		case prev == FileChangeAdded && status == FileChangeDeleted:
			delete(repo.pending, relPath) // canceled out
		case prev == FileChangeDeleted && status == FileChangeAdded:
			repo.pending[relPath] = FileChangeModified
		case status == FileChangeDeleted:
			repo.pending[relPath] = FileChangeDeleted
		case prev != FileChangeAdded:
			repo.pending[relPath] = FileChangeModified
		}
	}

	// (Re-)arm the debounce timer.
	if repo.timer != nil {
		repo.timer.Stop()
	}
	repo.timer = time.AfterFunc(w.debounce, func() { w.flush(repoID) })
}

// classify maps an absolute fsnotify path to a (repoID, repoPath,
// repo-relative path) tuple. Returns empty repoID when the path is
// not under any watched repo.
func (w *Watcher) classify(absEventPath string) (string, string, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Find the longest repoPath prefix.
	var bestRepoID, bestRepoPath string
	for path, id := range w.pathToID {
		if strings.HasPrefix(absEventPath, path) {
			if len(path) > len(bestRepoPath) {
				bestRepoPath = path
				bestRepoID = id
			}
		}
	}
	if bestRepoID == "" {
		return "", "", ""
	}
	rel, err := filepath.Rel(bestRepoPath, absEventPath)
	if err != nil {
		return "", "", ""
	}
	return bestRepoID, bestRepoPath, filepath.ToSlash(rel)
}

// classifyOp maps fsnotify op flags to the FileChangeStatus enum. CHMOD
// alone returns the empty string (we don't surface permission changes
// as deltas).
func classifyOp(op fsnotify.Op) FileChangeStatus {
	switch {
	case op&fsnotify.Create != 0:
		return FileChangeAdded
	case op&fsnotify.Remove != 0, op&fsnotify.Rename != 0:
		return FileChangeDeleted
	case op&fsnotify.Write != 0:
		return FileChangeModified
	}
	return ""
}

// flush converts the pending batch for repoID into a ChangeEvent and
// hands it to the router. Called from a timer goroutine.
func (w *Watcher) flush(repoID string) {
	w.mu.Lock()
	repo := w.repos[repoID]
	if repo == nil {
		w.mu.Unlock()
		return
	}
	if len(repo.pending) == 0 {
		repo.timer = nil
		w.mu.Unlock()
		return
	}
	// Drain pending under the lock so a concurrent handleEvent fold
	// doesn't double-route the same edits.
	pending := repo.pending
	repo.pending = make(map[string]FileChangeStatus)
	repo.timer = nil
	repoPath := repo.repoPath
	now := w.now()
	w.mu.Unlock()

	files := make([]FileChange, 0, len(pending))
	for path, status := range pending {
		change := FileChange{Path: path, Status: status}
		// Best-effort content hash for dedup with record_change. Skip
		// for deletions and on read errors. The hash makes
		// fsnotify+record_change observing the same edit collapse to
		// one routed event regardless of which connector won the race.
		if status != FileChangeDeleted {
			if content, err := os.ReadFile(filepath.Join(repoPath, path)); err == nil {
				h := sha256.Sum256(content)
				change.ContentHashAfter = "sha256:" + hex.EncodeToString(h[:])
			}
		}
		files = append(files, change)
	}

	// Determine branch from working-tree HEAD at flush time. If the
	// branch can't be resolved (non-git directory), drop the event;
	// the router would reject it via branch validation anyway.
	branch, err := git.HeadRef(repoPath)
	if err != nil {
		slog.Warn("changewatch.Watcher: branch detection failed; dropping batch",
			"repo_id", repoID,
			"err", err,
		)
		return
	}

	ev := &ChangeEvent{
		SchemaVersion: ChangeEventSchemaVersion,
		EventID:       fmt.Sprintf("fsnotify:%s:%d", repoID, now.UnixNano()),
		RepositoryID:  repoID,
		OccurredAt:    now,
		Branch:        branch,
		Files:         files,
		Source: ChangeSource{
			Kind:        SourceKindFsnotifyLocal,
			ConnectorID: "in_process:fsnotify",
			Actor:       "system:fsnotify",
		},
		Trust: Trust{
			Verified:           true,
			VerificationMethod: "in_process",
			ReceivedVia:        "in_process",
		},
	}

	// Submit on a background context. The router applies its own T0
	// timeout internally; the watcher's job is not to enforce a budget,
	// just to deliver.
	if _, err := w.router.Submit(context.Background(), ev); err != nil {
		// Most errors here are routine (rate limited, deduped, etc.) —
		// log at debug level so production logs aren't noisy. Real
		// problems (branch mismatch, containment) the router has
		// already logged at warn/error.
		slog.Debug("changewatch.Watcher: router submit returned",
			"repo_id", repoID,
			"event_id", ev.EventID,
			"err", err,
		)
	}
}

