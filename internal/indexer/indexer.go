// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/git"
)

// Indexer orchestrates repository indexing.
type Indexer struct {
	parser   *Parser
	progress func(ProgressEvent)
}

// NewIndexer creates a new Indexer.
func NewIndexer(progressFn func(ProgressEvent)) *Indexer {
	if progressFn == nil {
		progressFn = func(ProgressEvent) {} // no-op
	}
	return &Indexer{
		parser:   NewParser(),
		progress: progressFn,
	}
}

// ErrBranchMismatch is returned by IndexFiles when the caller's claimed
// branch does not match git.HeadRef(repoPath) on the working tree. The
// router (Phase 1.C) treats this as a structured rejection condition
// (rejected_branch_mismatch) and logs both branches for diagnosis. It
// is the Risk #4 condition the plan calls out: a CI push to main while
// an agent works on feature/x must not silently corrupt the agent's
// branch-scoped freshness state.
var ErrBranchMismatch = fmt.Errorf("indexer.IndexFiles: branch mismatch")

// ErrEmptyFiles is returned by IndexFiles when called with an empty
// files slice. The router (Phase 1.C) enforces the non-empty-delta
// guardrail at its own boundary; the indexer surfaces this as a
// programming error so a regression that lets an empty delta through
// the router is caught loudly.
var ErrEmptyFiles = fmt.Errorf("indexer.IndexFiles: files must be non-empty")

// ErrPreviousResultRequired is returned by IndexFiles when called with
// a nil previousResult. The router only invokes IndexFiles after a
// prior IndexRepository / IndexRepositoryIncremental has produced an
// IndexResult; calling without one is a programming error.
var ErrPreviousResultRequired = fmt.Errorf("indexer.IndexFiles: previousResult must be non-nil")

// IndexFiles re-parses only the listed files and merges the result
// into a copy of previousResult. The branch argument is required and
// recorded on the returned IndexResult so freshness propagates
// correctly: a CI push to main while an agent works on feature/x must
// not mark the agent's context stale on the wrong branch.
//
// Behavior:
//   - Validates branch against git.HeadRef(repoPath). On mismatch,
//     returns ErrBranchMismatch with both branches in the wrapped error
//     message; the router translates that to rejected_branch_mismatch.
//   - For each file in `files`, runs the same per-file parse path
//     IndexRepositoryIncremental uses internally (read → tree-sitter
//     parse → content-hash → AI-generated heuristics). Does NOT walk
//     the rest of the repo: walking is the cost IndexRepositoryIncremental
//     cannot escape, and it blows the 100ms T0 budget on any non-trivial
//     repo even when most files hash-skip.
//   - A file that does not exist on disk is treated as a deletion: it
//     is removed from the merged file set.
//   - A file whose extension is not a recognized language (per
//     git.DetectLanguage) is skipped — kept as-is in the merged set if
//     it was previously indexed, dropped if it is new.
//   - A read or parse error for a single file does not fail the whole
//     batch; the error is appended to result.Errors and the file is
//     left as-is in the merged set (carry forward the prior FileResult
//     if any).
//   - After merging, recomputes per-IndexResult aggregates (TotalFiles,
//     TotalSymbols, Modules, Relations, TotalRelations) over the merged
//     file set so downstream consumers (freshness envelope, MCP reads)
//     see internally-consistent state. The aggregate recompute is the
//     dominant cost; the pre-flight spike measured ~10ms for the call
//     graph on a 500-file fixture, leaving ~7x headroom under the 100ms
//     budget.
//   - previousResult is never mutated; the merged result is a fresh
//     IndexResult with copies of the carried-forward FileResults.
//
// IndexFiles is the only entry point the change-watch router (Phase
// 1.C) calls. IndexRepository (full reindex, gated by RepoIndexFullReason)
// and IndexRepositoryIncremental (full-tree incremental scan) are
// unchanged for their existing callers — the router cannot reach them.
func (idx *Indexer) IndexFiles(
	ctx context.Context,
	repoPath string,
	files []string,
	branch string,
	previousResult *IndexResult,
) (*IndexResult, error) {
	if len(files) == 0 {
		return nil, ErrEmptyFiles
	}
	if previousResult == nil {
		return nil, ErrPreviousResultRequired
	}

	// Branch validation: HeadRef returns ErrNotAGitRepo for a non-git
	// directory; the router never invokes IndexFiles on such a path
	// (the watcher only fires for indexed repos, which are always git
	// working trees), so we surface that as a regular wrapped error
	// rather than a special case. A real branch mismatch (the
	// load-bearing Risk #4 condition) returns ErrBranchMismatch.
	headBranch, err := git.HeadRef(repoPath)
	if err != nil {
		return nil, fmt.Errorf("validating branch against working tree: %w", err)
	}
	if headBranch != branch {
		return nil, fmt.Errorf("%w: claimed=%q head=%q", ErrBranchMismatch, branch, headBranch)
	}

	// Build a path → index lookup over previousResult.Files so the
	// per-file merge is O(len(files)) rather than O(len(files) * |prev|).
	prevByPath := make(map[string]int, len(previousResult.Files))
	for i, f := range previousResult.Files {
		prevByPath[f.Path] = i
	}

	// Materialize a fresh slice with all the carry-forward entries up
	// front. We will overwrite or drop entries as we process the
	// affected files; new files (not in prevByPath) get appended.
	merged := make([]FileResult, len(previousResult.Files))
	copy(merged, previousResult.Files)
	// Track positions to drop (deletions). Process in a second pass so
	// indices stay stable while we iterate the affected-file list.
	dropIndices := map[int]bool{}

	var perFileErrors []string

	for _, relPath := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		absPath := filepath.Join(repoPath, relPath)

		// Deletion: file no longer exists on disk.
		if _, statErr := osStatFn(absPath); statErr != nil {
			if osIsNotExistFn(statErr) {
				if idx, ok := prevByPath[relPath]; ok {
					dropIndices[idx] = true
				}
				// Otherwise: a file we never had and that doesn't exist;
				// nothing to do.
				continue
			}
			perFileErrors = append(perFileErrors, fmt.Sprintf("stat %s: %s", relPath, statErr))
			continue
		}

		language := git.DetectLanguage(relPath)
		if GetLanguageConfig(language) == nil {
			// Unknown / unsupported language: keep any prior entry as-is
			// (so a previously-indexed file with a now-unsupported
			// extension doesn't accidentally drop out), drop net-new
			// unsupported files.
			continue
		}

		content, readErr := git.ReadFile(absPath)
		if readErr != nil {
			perFileErrors = append(perFileErrors, fmt.Sprintf("read %s: %s", relPath, readErr))
			continue
		}

		fileResult, parseErr := idx.parser.ParseFile(ctx, relPath, language, content)
		if parseErr != nil {
			perFileErrors = append(perFileErrors, fmt.Sprintf("parse %s: %s", relPath, parseErr))
			continue
		}

		hash := sha256.Sum256(content)
		fileResult.ContentHash = hex.EncodeToString(hash[:])

		aiResult := DetectAIGenerated(string(content), language, fileResult.Symbols)
		fileResult.AIScore = aiResult.Score
		fileResult.AISignals = aiResult.Signals

		if i, ok := prevByPath[relPath]; ok {
			merged[i] = *fileResult
			delete(dropIndices, i) // a carry-forward we updated is not a drop
		} else {
			merged = append(merged, *fileResult)
		}
	}

	// Apply deletions. Build a new slice rather than slicing in place so
	// the caller's previousResult.Files backing array is never touched.
	if len(dropIndices) > 0 {
		filtered := make([]FileResult, 0, len(merged)-len(dropIndices))
		for i, f := range merged {
			if dropIndices[i] {
				continue
			}
			filtered = append(filtered, f)
		}
		merged = filtered
	}

	// Build the fresh result. Carry forward repo identity from the
	// previous result, record the new branch, and recompute aggregates
	// over the merged file set.
	result := &IndexResult{
		RepoName: previousResult.RepoName,
		RepoPath: previousResult.RepoPath,
		Branch:   branch,
		Files:    merged,
	}
	result.TotalFiles = len(result.Files)
	for _, f := range result.Files {
		result.TotalSymbols += len(f.Symbols)
	}

	// Carry forward errors that survived the merge: every error from a
	// previousResult.Files entry whose path wasn't in the affected-file
	// set is still relevant; perFileErrors are this call's incremental
	// errors. Errors recorded against an affected file in previousResult
	// are dropped because we just re-parsed that file successfully (or
	// recorded a fresh error).
	affected := make(map[string]bool, len(files))
	for _, p := range files {
		affected[p] = true
	}
	for _, prevErr := range previousResult.Errors {
		// Carry forward any prior error whose subject file wasn't in
		// the affected set. The error format isn't structured enough
		// for path extraction in every case; carry forward the whole
		// list and dedup at result-consumer level if needed. The
		// alternative — try to parse the error string — is fragile and
		// not worth the complexity for a list that is typically empty.
		_ = prevErr
	}
	// Pragmatic choice: do not carry forward previousResult.Errors. The
	// freshness envelope on MCP reads will surface only the most recent
	// IndexResult's errors, and the previous indexer pass already
	// reported its own errors at its own slog/log site. Re-reporting
	// them here risks double-counting in downstream telemetry.
	result.Errors = perFileErrors

	// Modules: cheap O(n) walk over file paths. Recompute over the
	// merged set so a deletion or new package directory is reflected.
	result.Modules = ExtractModules(result.Files)

	// Call graph + test linkage: recompute over the merged set so
	// cross-file edges that the per-file delta invalidated (e.g. the
	// affected file no longer exports a symbol that other files were
	// linked to) are re-resolved correctly. This is the dominant cost;
	// the spike measured ~10ms on a 500-file fixture.
	result.Relations = idx.resolveCallGraph(result)
	result.Relations = append(result.Relations, idx.resolveTestLinkage(result)...)
	result.TotalRelations = idx.countRelations(result) + len(result.Relations)

	return result, nil
}

// osStatFn / osIsNotExistFn are package-level vars so the tests in
// this package can swap out the filesystem boundary without bringing
// in a heavyweight fs abstraction. They default to the real os
// functions and are not part of the exported API.
var (
	osStatFn       = os.Stat
	osIsNotExistFn = os.IsNotExist
)

// IndexRepository scans and indexes a local repository.
//
// reason is required and must be one of the named RepoIndexFullReason
// constants (ReasonInitialOnboard or ReasonOperatorRebuild). The guard
// exists to keep the change-watch router (Phase 1.C) from accidentally
// reaching this whole-tree path: a router-driven invocation has no
// legitimate reason value to pass, so it can't compile against the
// signature, much less call it. See
// thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md (v5,
// "Audit of latent full-reindex paths") for the full audit context
// and Phase 1 done-definition test #10 for the runtime assertion.
func (idx *Indexer) IndexRepository(ctx context.Context, repoPath string, reason RepoIndexFullReason) (*IndexResult, error) {
	if err := validateFullReindexReason(reason); err != nil {
		slog.Error("IndexRepository called without valid reason", "reason", reason.String(), "repo_path", repoPath)
		return nil, err
	}
	repoID := uuid.New().String()

	// Phase 1: Scan
	idx.progress(ProgressEvent{
		RepoID:      repoID,
		Phase:       "scanning",
		Description: "Scanning repository files",
		Progress:    0.0,
	})

	repo, err := git.ScanRepository(repoPath)
	if err != nil {
		return nil, fmt.Errorf("scanning repository: %w", err)
	}

	slog.Info("scanned repository", "name", repo.Name, "files", len(repo.Files))

	result := &IndexResult{
		RepoName: repo.Name,
		RepoPath: repo.Path,
	}

	// Phase 2: Parse each file
	totalFiles := len(repo.Files)
	for i, fileInfo := range repo.Files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		idx.progress(ProgressEvent{
			RepoID:      repoID,
			Phase:       "parsing",
			Current:     i + 1,
			Total:       totalFiles,
			File:        fileInfo.Path,
			Description: fmt.Sprintf("Parsing %s", fileInfo.Path),
			Progress:    float64(i) / float64(totalFiles) * 0.8, // 0-80% for parsing
		})

		// Only parse supported languages
		if GetLanguageConfig(fileInfo.Language) == nil {
			continue
		}

		content, err := git.ReadFile(fileInfo.AbsPath)
		if err != nil {
			slog.Warn("failed to read file", "path", fileInfo.Path, "error", err)
			result.Errors = append(result.Errors, fmt.Sprintf("read %s: %s", fileInfo.Path, err))
			continue
		}

		fileResult, err := idx.parser.ParseFile(ctx, fileInfo.Path, fileInfo.Language, content)
		if err != nil {
			slog.Warn("failed to parse file", "path", fileInfo.Path, "error", err)
			result.Errors = append(result.Errors, fmt.Sprintf("parse %s: %s", fileInfo.Path, err))
			continue
		}

		// Compute content hash for incremental indexing
		hash := sha256.Sum256(content)
		fileResult.ContentHash = hex.EncodeToString(hash[:])

		// AI-generated code detection
		aiResult := DetectAIGenerated(string(content), fileInfo.Language, fileResult.Symbols)
		fileResult.AIScore = aiResult.Score
		fileResult.AISignals = aiResult.Signals

		result.Files = append(result.Files, *fileResult)
		result.TotalSymbols += len(fileResult.Symbols)
	}

	result.TotalFiles = len(result.Files)

	// Phase 3: Extract modules
	idx.progress(ProgressEvent{
		RepoID:      repoID,
		Phase:       "modules",
		Description: "Extracting modules",
		Progress:    0.85,
	})
	result.Modules = ExtractModules(result.Files)

	// Phase 4: Resolve call graph with scoped matching
	idx.progress(ProgressEvent{
		RepoID:      repoID,
		Phase:       "relations",
		Description: "Resolving call graph",
		Progress:    0.9,
	})
	result.Relations = idx.resolveCallGraph(result)
	result.Relations = append(result.Relations, idx.resolveTestLinkage(result)...)
	result.TotalRelations = idx.countRelations(result) + len(result.Relations)

	// Done
	idx.progress(ProgressEvent{
		RepoID:      repoID,
		Phase:       "complete",
		Description: "Indexing complete",
		Progress:    1.0,
		Current:     totalFiles,
		Total:       totalFiles,
	})

	slog.Info("indexing complete",
		"repo", repo.Name,
		"files", result.TotalFiles,
		"symbols", result.TotalSymbols,
		"modules", len(result.Modules),
		"relations", result.TotalRelations,
	)

	return result, nil
}

// IndexRepositoryIncremental re-indexes a repository, skipping files whose content
// hasn't changed. previousHashes maps filePath → contentHash from the last index.
// Unchanged files are carried forward as-is from previousFiles.
func (idx *Indexer) IndexRepositoryIncremental(ctx context.Context, repoPath string, previousHashes map[string]string, previousFiles map[string]FileResult) (*IndexResult, error) {
	repoID := uuid.New().String()

	idx.progress(ProgressEvent{
		RepoID:      repoID,
		Phase:       "scanning",
		Description: "Scanning repository files",
		Progress:    0.0,
	})

	repo, err := git.ScanRepository(repoPath)
	if err != nil {
		return nil, fmt.Errorf("scanning repository: %w", err)
	}

	slog.Info("incremental scan", "name", repo.Name, "files", len(repo.Files))

	result := &IndexResult{
		RepoName: repo.Name,
		RepoPath: repo.Path,
	}

	totalFiles := len(repo.Files)
	reused := 0
	for i, fileInfo := range repo.Files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		idx.progress(ProgressEvent{
			RepoID:   repoID,
			Phase:    "parsing",
			Current:  i + 1,
			Total:    totalFiles,
			File:     fileInfo.Path,
			Progress: float64(i) / float64(totalFiles) * 0.8,
		})

		if GetLanguageConfig(fileInfo.Language) == nil {
			continue
		}

		content, err := git.ReadFile(fileInfo.AbsPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("read %s: %s", fileInfo.Path, err))
			continue
		}

		hash := sha256.Sum256(content)
		hashStr := hex.EncodeToString(hash[:])

		// Reuse previous result if hash matches
		if prevHash, ok := previousHashes[fileInfo.Path]; ok && prevHash == hashStr {
			if prevFile, ok := previousFiles[fileInfo.Path]; ok {
				result.Files = append(result.Files, prevFile)
				result.TotalSymbols += len(prevFile.Symbols)
				reused++
				continue
			}
		}

		fileResult, err := idx.parser.ParseFile(ctx, fileInfo.Path, fileInfo.Language, content)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("parse %s: %s", fileInfo.Path, err))
			continue
		}

		fileResult.ContentHash = hashStr

		// AI-generated code detection
		aiResult := DetectAIGenerated(string(content), fileInfo.Language, fileResult.Symbols)
		fileResult.AIScore = aiResult.Score
		fileResult.AISignals = aiResult.Signals

		result.Files = append(result.Files, *fileResult)
		result.TotalSymbols += len(fileResult.Symbols)
	}

	result.TotalFiles = len(result.Files)

	slog.Info("incremental indexing", "reused", reused, "reparsed", result.TotalFiles-reused)

	// Modules and call graph
	idx.progress(ProgressEvent{RepoID: repoID, Phase: "modules", Progress: 0.85})
	result.Modules = ExtractModules(result.Files)

	idx.progress(ProgressEvent{RepoID: repoID, Phase: "relations", Progress: 0.9})
	result.Relations = idx.resolveCallGraph(result)
	result.Relations = append(result.Relations, idx.resolveTestLinkage(result)...)
	result.TotalRelations = idx.countRelations(result) + len(result.Relations)

	idx.progress(ProgressEvent{RepoID: repoID, Phase: "complete", Progress: 1.0})

	slog.Info("incremental indexing complete",
		"repo", repo.Name,
		"files", result.TotalFiles,
		"symbols", result.TotalSymbols,
		"reused", reused,
	)

	return result, nil
}

// resolveCallGraph resolves call sites to target symbols using scoped matching.
// Resolution strategy (per plan): same-file > same-package > unambiguous global.
// Ambiguous matches are skipped to avoid confidently wrong call edges.
func (idx *Indexer) resolveCallGraph(result *IndexResult) []Relation {
	// Map symbol name → []symbolEntry for all callable symbols
	nameIndex := make(map[string][]symbolEntry)
	for _, f := range result.Files {
		dir := filepath.Dir(f.Path)
		for _, sym := range f.Symbols {
			if sym.Kind == SymbolFunction || sym.Kind == SymbolMethod || sym.Kind == SymbolTest {
				nameIndex[sym.Name] = append(nameIndex[sym.Name], symbolEntry{
					id:       sym.ID,
					filePath: sym.FilePath,
					dir:      dir,
				})
			}
		}
	}

	// Build caller ID → filePath lookup
	callerFile := make(map[string]string)
	for _, f := range result.Files {
		for _, sym := range f.Symbols {
			callerFile[sym.ID] = f.Path
		}
	}

	seen := make(map[string]bool) // "callerID:targetID" dedup
	var relations []Relation

	for _, f := range result.Files {
		callerDir := filepath.Dir(f.Path)

		for _, call := range f.Calls {
			candidates := nameIndex[call.CalleeName]
			if len(candidates) == 0 {
				continue
			}

			// Don't resolve self-calls (call within the same symbol)
			target := resolveCallTargetScoped(call.CallerID, call.FilePath, callerDir, candidates)
			if target == "" || target == call.CallerID {
				continue
			}

			key := call.CallerID + ":" + target
			if seen[key] {
				continue
			}
			seen[key] = true

			relations = append(relations, Relation{
				SourceID: call.CallerID,
				TargetID: target,
				Type:     RelationCalls,
			})
		}
	}

	slog.Info("call graph resolved", "edges", len(relations))
	return relations
}

// resolveTestLinkage walks the call sites once more, this time
// looking for calls whose *caller* is a test symbol. For each such
// call where the callee can be resolved to a non-test symbol in the
// same repo, emit a RelationTests edge {source: test caller, target:
// symbol-being-tested}. This is how get_tests_for_symbol's
// persisted_edge source gets populated.
//
// Conservative matching: only exact name matches in the same repo,
// and only when the resolved target isn't itself a test. The
// resolver doesn't try to infer intent beyond "a test function
// directly calls this symbol."
func (idx *Indexer) resolveTestLinkage(result *IndexResult) []Relation {
	// symbolID → Symbol (so we can check IsTest on the resolved target).
	byID := make(map[string]*Symbol)
	for i, f := range result.Files {
		for j := range f.Symbols {
			byID[f.Symbols[j].ID] = &result.Files[i].Symbols[j]
		}
	}

	// Name-index for callable non-test symbols (the resolution
	// targets). Tests aren't candidates for their own tests.
	nameIndex := make(map[string][]symbolEntry)
	for _, f := range result.Files {
		dir := filepath.Dir(f.Path)
		for _, sym := range f.Symbols {
			if sym.IsTest {
				continue
			}
			if sym.Kind == SymbolFunction || sym.Kind == SymbolMethod {
				nameIndex[sym.Name] = append(nameIndex[sym.Name], symbolEntry{
					id:       sym.ID,
					filePath: sym.FilePath,
					dir:      dir,
				})
			}
		}
	}

	seen := make(map[string]bool)
	var relations []Relation

	for _, f := range result.Files {
		callerDir := filepath.Dir(f.Path)
		for _, call := range f.Calls {
			caller := byID[call.CallerID]
			if caller == nil || !caller.IsTest {
				continue
			}
			candidates := nameIndex[call.CalleeName]
			if len(candidates) == 0 {
				continue
			}
			target := resolveCallTargetScoped(call.CallerID, call.FilePath, callerDir, candidates)
			if target == "" || target == call.CallerID {
				continue
			}
			// Skip targets that are themselves test symbols —
			// "test calls test helper" isn't the intent.
			if tsym, ok := byID[target]; ok && tsym.IsTest {
				continue
			}

			key := call.CallerID + ":" + target
			if seen[key] {
				continue
			}
			seen[key] = true

			relations = append(relations, Relation{
				SourceID: call.CallerID,
				TargetID: target,
				Type:     RelationTests,
			})
		}
	}

	slog.Info("test linkage resolved", "edges", len(relations))
	return relations
}

// resolveCallTargetScoped applies scoped resolution:
// 1. Same file match (only if unambiguous within the file)
// 2. Same package/directory match (only if unambiguous within the package)
// 3. Global match (only if exactly one candidate across all files)
func resolveCallTargetScoped(callerID, callerFilePath, callerDir string, candidates []symbolEntry) string {
	// Filter out the caller itself
	var filtered []symbolEntry
	for _, c := range candidates {
		if c.id != callerID {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return ""
	}

	// 1. Same-file matches
	var sameFile []symbolEntry
	for _, c := range filtered {
		if c.filePath == callerFilePath {
			sameFile = append(sameFile, c)
		}
	}
	if len(sameFile) == 1 {
		return sameFile[0].id
	}

	// 2. Same-package matches
	var samePackage []symbolEntry
	for _, c := range filtered {
		if c.dir == callerDir {
			samePackage = append(samePackage, c)
		}
	}
	if len(samePackage) == 1 {
		return samePackage[0].id
	}

	// 3. Unambiguous global
	if len(filtered) == 1 {
		return filtered[0].id
	}

	// Ambiguous — do not emit a relation
	return ""
}

type symbolEntry struct {
	id       string
	filePath string
	dir      string
}

func (idx *Indexer) countRelations(result *IndexResult) int {
	count := 0

	// Count contains relations (file -> symbol)
	for _, f := range result.Files {
		count += len(f.Symbols) // Each symbol is contained by its file
		count += len(f.Imports) // Each import is a relation
	}

	// Count part_of relations (file -> module)
	count += len(result.Modules)

	return count
}

// ExtractModules derives module information from the file structure.
func ExtractModules(files []FileResult) []Module {
	dirFiles := make(map[string]int)
	for _, f := range files {
		dir := filepath.Dir(f.Path)
		dirFiles[dir]++
	}

	var modules []Module
	for dir, count := range dirFiles {
		name := dir
		if name == "." {
			name = "root"
		}
		// Use last path component as module name
		parts := strings.Split(dir, string(filepath.Separator))
		if len(parts) > 0 {
			name = parts[len(parts)-1]
		}

		modules = append(modules, Module{
			ID:        uuid.New().String(),
			Name:      name,
			Path:      dir,
			FileCount: count,
		})
	}

	return modules
}
