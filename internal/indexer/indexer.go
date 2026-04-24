// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
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

// IndexRepository scans and indexes a local repository.
func (idx *Indexer) IndexRepository(ctx context.Context, repoPath string) (*IndexResult, error) {
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
