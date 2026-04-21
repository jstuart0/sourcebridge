// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// Repository represents a stored repository.
type Repository struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Path                  string    `json:"path"`
	ClonePath             string    `json:"clone_path,omitempty"` // local path to persisted clone (for remote repos)
	RemoteURL             string    `json:"remote_url,omitempty"` // canonical remote git URL
	CommitSHA             string    `json:"commit_sha,omitempty"`
	PreviousCommitSHA     string    `json:"previous_commit_sha,omitempty"`
	Branch                string    `json:"branch,omitempty"`
	GenerationModeDefault string    `json:"generation_mode_default,omitempty"`
	Status                string    `json:"status"` // pending, indexing, ready, error
	FileCount             int       `json:"file_count"`
	FunctionCount         int       `json:"function_count"`
	ClassCount            int       `json:"class_count"`
	LastIndexedAt         time.Time `json:"last_indexed_at"`
	CreatedAt             time.Time `json:"created_at"`
	IndexError            string    `json:"index_error,omitempty"`
	AuthToken             string    `json:"-"` // never serialized — holds PAT for private repo access

	// Cached understanding score — precomputed on reindex/link/review changes.
	UnderstandingScoreVal *float64   `json:"understanding_score,omitempty"`
	UnderstandingScoreAt  *time.Time `json:"understanding_score_at,omitempty"`
}

// File represents a stored file.
type File struct {
	ID          string   `json:"id"`
	RepoID      string   `json:"repo_id"`
	Path        string   `json:"path"`
	Language    string   `json:"language"`
	LineCount   int      `json:"line_count"`
	ContentHash string   `json:"content_hash,omitempty"`
	AIScore     float64  `json:"ai_score"`
	AISignals   []string `json:"ai_signals,omitempty"`
}

// StoredSymbol is a symbol stored in the graph.
type StoredSymbol struct {
	ID            string `json:"id"`
	RepoID        string `json:"repo_id"`
	FileID        string `json:"file_id"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Language      string `json:"language"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	Signature     string `json:"signature"`
	DocComment    string `json:"doc_comment"`
	IsTest        bool   `json:"is_test"`
}

// StoredModule is a module stored in the graph.
type StoredModule struct {
	ID        string `json:"id"`
	RepoID    string `json:"repo_id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	FileCount int    `json:"file_count"`
}

// StoredImport is an import relationship.
type StoredImport struct {
	FileID string `json:"file_id"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
}

// StoredLink is a requirement-code link stored in the graph.
type StoredLink struct {
	ID            string    `json:"id"`
	RepoID        string    `json:"repo_id"`
	RequirementID string    `json:"requirement_id"`
	SymbolID      string    `json:"symbol_id"`
	Confidence    float64   `json:"confidence"`
	Source        string    `json:"source"` // comment, semantic, reference, test, manual
	LinkType      string    `json:"link_type"`
	Rationale     string    `json:"rationale"`
	Verified      bool      `json:"verified"`
	VerifiedBy    string    `json:"verified_by,omitempty"`
	Rejected      bool      `json:"rejected"`
	CreatedAt     time.Time `json:"created_at"`
}

// StoredRequirement is a requirement stored in the graph.
type StoredRequirement struct {
	ID                 string    `json:"id"`
	RepoID             string    `json:"repo_id"`
	ExternalID         string    `json:"external_id"`
	Title              string    `json:"title"`
	Description        string    `json:"description"`
	Source             string    `json:"source"`
	Priority           string    `json:"priority"`
	Tags               []string  `json:"tags"`
	AcceptanceCriteria []string  `json:"acceptance_criteria"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// RequirementUpdate carries the patch fields for UpdateRequirementFields.
// Nil pointers mean "do not modify" so callers can partially-update
// without needing to read-before-write.
type RequirementUpdate struct {
	ExternalID         *string
	Title              *string
	Description        *string
	Priority           *string
	Source             *string
	Tags               *[]string
	AcceptanceCriteria *[]string
}

// DiscoveredRequirement represents a spec inferred from code analysis.
type DiscoveredRequirement struct {
	ID              string    `json:"id"`
	RepoID          string    `json:"repo_id"`
	Source          string    `json:"source"` // "test", "schema", "comment"
	SourceFile      string    `json:"source_file"`
	SourceLine      int       `json:"source_line"`
	SourceFiles     []string  `json:"source_files"` // additional files (from dedup merge)
	Text            string    `json:"text"`         // refined requirement text
	RawText         string    `json:"raw_text"`     // original extraction
	GroupKey        string    `json:"group_key"`
	Language        string    `json:"language"`
	Keywords        []string  `json:"keywords"`
	Confidence      string    `json:"confidence"` // "high", "medium", "low"
	Status          string    `json:"status"`     // "discovered", "promoted", "dismissed"
	LLMRefined      bool      `json:"llm_refined"`
	PromotedTo      string    `json:"promoted_to,omitempty"`
	DismissedBy     string    `json:"dismissed_by,omitempty"`
	DismissedReason string    `json:"dismissed_reason,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	DismissedAt     time.Time `json:"dismissed_at,omitempty"`
	PromotedAt      time.Time `json:"promoted_at,omitempty"`
}

// LLMUsageRecord tracks a single LLM API call for cost/usage monitoring.
type LLMUsageRecord struct {
	ID           string    `json:"id"`
	RepoID       string    `json:"repo_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	Operation    string    `json:"operation"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CreatedAt    time.Time `json:"created_at"`
}

// EmbeddingRecord stores a cached embedding vector for a symbol or requirement.
type EmbeddingRecord struct {
	ID         string    `json:"id"`
	TargetID   string    `json:"target_id"`
	TargetType string    `json:"target_type"` // "symbol", "requirement", "file"
	Vector     []float32 `json:"vector"`
	Dimension  int       `json:"dimension"`
	Model      string    `json:"model"`
	TextHash   string    `json:"text_hash"`
	CreatedAt  time.Time `json:"created_at"`
}

// ReviewResultRecord stores a persisted AI code review.
type ReviewResultRecord struct {
	ID        string          `json:"id"`
	RepoID    string          `json:"repo_id"`
	TargetID  string          `json:"target_id"` // symbol or file ID
	Template  string          `json:"template"`
	Findings  []ReviewFinding `json:"findings"`
	Score     *float64        `json:"score,omitempty"`
	CreatedBy string          `json:"created_by,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// ReviewFinding is a single finding from an AI review.
type ReviewFinding struct {
	Severity   string `json:"severity"` // "info", "warning", "error", "critical"
	Category   string `json:"category"`
	Message    string `json:"message"`
	Line       int    `json:"line,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Store provides graph storage operations.
// In-memory implementation for OSS/CLI mode.
// SurrealDB backend can be plugged in for Docker/K8s mode.
type Store struct {
	mu                     sync.RWMutex
	repos                  map[string]*Repository
	files                  map[string]*File              // fileID -> File
	symbols                map[string]*StoredSymbol      // symbolID -> Symbol
	modules                map[string]*StoredModule      // moduleID -> Module
	requirements           map[string]*StoredRequirement // reqID -> Requirement
	links                  map[string]*StoredLink        // linkID -> Link
	imports                []StoredImport
	callGraph              map[string][]string // callerID -> []calleeID
	reverseCallGraph       map[string][]string // calleeID -> []callerID
	repoFiles              map[string][]string // repoID -> []fileID
	repoSymbols            map[string][]string // repoID -> []symbolID
	repoModules            map[string][]string // repoID -> []moduleID
	repoRequirements       map[string][]string // repoID -> []reqID
	repoLinks              map[string][]string // repoID -> []linkID
	reqLinks               map[string][]string // reqID -> []linkID
	symLinks               map[string][]string // symbolID -> []linkID
	fileSymbols            map[string][]string // fileID -> []symbolID
	llmUsage               []LLMUsageRecord
	embeddings             map[string]*EmbeddingRecord       // targetID -> EmbeddingRecord
	reviewResults          map[string]*ReviewResultRecord    // reviewID -> ReviewResultRecord
	impactReports          map[string][]*ImpactReport        // repoID -> []*ImpactReport
	discoveredRequirements map[string]*DiscoveredRequirement // discReqID -> DiscoveredRequirement
	repoDiscoveredReqs     map[string][]string               // repoID -> []discReqID
}

// NewStore creates a new in-memory graph store.
func NewStore() *Store {
	return &Store{
		repos:                  make(map[string]*Repository),
		files:                  make(map[string]*File),
		symbols:                make(map[string]*StoredSymbol),
		modules:                make(map[string]*StoredModule),
		requirements:           make(map[string]*StoredRequirement),
		links:                  make(map[string]*StoredLink),
		callGraph:              make(map[string][]string),
		reverseCallGraph:       make(map[string][]string),
		repoFiles:              make(map[string][]string),
		repoSymbols:            make(map[string][]string),
		repoModules:            make(map[string][]string),
		repoRequirements:       make(map[string][]string),
		repoLinks:              make(map[string][]string),
		reqLinks:               make(map[string][]string),
		symLinks:               make(map[string][]string),
		fileSymbols:            make(map[string][]string),
		embeddings:             make(map[string]*EmbeddingRecord),
		reviewResults:          make(map[string]*ReviewResultRecord),
		impactReports:          make(map[string][]*ImpactReport),
		discoveredRequirements: make(map[string]*DiscoveredRequirement),
		repoDiscoveredReqs:     make(map[string][]string),
	}
}

// CreateRepository creates a placeholder repository with PENDING status.
func (s *Store) CreateRepository(name, path string) (*Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	repo := &Repository{
		ID:        uuid.New().String(),
		Name:      name,
		Path:      path,
		Status:    "pending",
		CreatedAt: now,
	}
	s.repos[repo.ID] = repo
	return repo, nil
}

// StoreIndexResult persists a full indexing result to the graph.
func (s *Store) StoreIndexResult(result *indexer.IndexResult) (*Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	repoID := uuid.New().String()

	funcCount := 0
	classCount := 0

	// Map indexer symbol IDs → store symbol IDs for call graph resolution
	idMap := make(map[string]string)

	// Store files and symbols
	for _, fr := range result.Files {
		fileID := uuid.New().String()
		s.files[fileID] = &File{
			ID:          fileID,
			RepoID:      repoID,
			Path:        fr.Path,
			Language:    fr.Language,
			LineCount:   fr.LineCount,
			ContentHash: fr.ContentHash,
			AIScore:     fr.AIScore,
			AISignals:   fr.AISignals,
		}
		s.repoFiles[repoID] = append(s.repoFiles[repoID], fileID)

		for _, sym := range fr.Symbols {
			symID := uuid.New().String()
			idMap[sym.ID] = symID
			s.symbols[symID] = &StoredSymbol{
				ID:            symID,
				RepoID:        repoID,
				FileID:        fileID,
				Name:          sym.Name,
				QualifiedName: sym.QualifiedName,
				Kind:          string(sym.Kind),
				Language:      sym.Language,
				FilePath:      sym.FilePath,
				StartLine:     sym.StartLine,
				EndLine:       sym.EndLine,
				Signature:     sym.Signature,
				DocComment:    sym.DocComment,
				IsTest:        sym.IsTest,
			}
			s.repoSymbols[repoID] = append(s.repoSymbols[repoID], symID)
			s.fileSymbols[fileID] = append(s.fileSymbols[fileID], symID)

			switch sym.Kind {
			case indexer.SymbolFunction, indexer.SymbolMethod:
				funcCount++
			case indexer.SymbolClass, indexer.SymbolStruct, indexer.SymbolInterface, indexer.SymbolEnum, indexer.SymbolTrait:
				classCount++
			}
		}

		for _, imp := range fr.Imports {
			s.imports = append(s.imports, StoredImport{
				FileID: fileID,
				Path:   imp.Path,
				Line:   imp.Line,
			})
		}
	}

	// Store call graph relations
	for _, rel := range result.Relations {
		if rel.Type != indexer.RelationCalls {
			continue
		}
		callerID := idMap[rel.SourceID]
		calleeID := idMap[rel.TargetID]
		if callerID == "" || calleeID == "" {
			continue
		}
		s.callGraph[callerID] = append(s.callGraph[callerID], calleeID)
		s.reverseCallGraph[calleeID] = append(s.reverseCallGraph[calleeID], callerID)
	}

	// Store modules
	for _, mod := range result.Modules {
		modID := uuid.New().String()
		s.modules[modID] = &StoredModule{
			ID:        modID,
			RepoID:    repoID,
			Name:      mod.Name,
			Path:      mod.Path,
			FileCount: mod.FileCount,
		}
		s.repoModules[repoID] = append(s.repoModules[repoID], modID)
	}

	// Store repository
	now := time.Now()
	repo := &Repository{
		ID:            repoID,
		Name:          result.RepoName,
		Path:          result.RepoPath,
		Status:        "ready",
		FileCount:     result.TotalFiles,
		FunctionCount: funcCount,
		ClassCount:    classCount,
		LastIndexedAt: now,
		CreatedAt:     now,
	}
	s.repos[repoID] = repo

	return repo, nil
}

// ReplaceIndexResult atomically replaces all files, symbols, modules, and relations
// for an existing repository with new index results.
func (s *Store) ReplaceIndexResult(repoID string, result *indexer.IndexResult) (*Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	repo := s.repos[repoID]
	if repo == nil {
		return nil, fmt.Errorf("repository not found: %s", repoID)
	}

	// Remove old data for this repo (without lock, already held)
	for _, symID := range s.repoSymbols[repoID] {
		for _, calleeID := range s.callGraph[symID] {
			s.reverseCallGraph[calleeID] = removeFromSlice(s.reverseCallGraph[calleeID], symID)
		}
		delete(s.callGraph, symID)
		for _, callerID := range s.reverseCallGraph[symID] {
			s.callGraph[callerID] = removeFromSlice(s.callGraph[callerID], symID)
		}
		delete(s.reverseCallGraph, symID)
		delete(s.symbols, symID)
	}
	delete(s.repoSymbols, repoID)

	for _, fileID := range s.repoFiles[repoID] {
		delete(s.files, fileID)
		delete(s.fileSymbols, fileID)
	}
	delete(s.repoFiles, repoID)

	for _, modID := range s.repoModules[repoID] {
		delete(s.modules, modID)
	}
	delete(s.repoModules, repoID)

	// Remove old imports for this repo's files
	var keptImports []StoredImport
	oldFileIDs := make(map[string]bool)
	for _, fid := range s.repoFiles[repoID] {
		oldFileIDs[fid] = true
	}
	for _, imp := range s.imports {
		if !oldFileIDs[imp.FileID] {
			keptImports = append(keptImports, imp)
		}
	}
	s.imports = keptImports

	// Re-insert new data
	funcCount := 0
	classCount := 0
	idMap := make(map[string]string)

	for _, fr := range result.Files {
		fileID := uuid.New().String()
		s.files[fileID] = &File{
			ID:          fileID,
			RepoID:      repoID,
			Path:        fr.Path,
			Language:    fr.Language,
			LineCount:   fr.LineCount,
			ContentHash: fr.ContentHash,
			AIScore:     fr.AIScore,
			AISignals:   fr.AISignals,
		}
		s.repoFiles[repoID] = append(s.repoFiles[repoID], fileID)

		for _, sym := range fr.Symbols {
			symID := uuid.New().String()
			idMap[sym.ID] = symID
			s.symbols[symID] = &StoredSymbol{
				ID:            symID,
				RepoID:        repoID,
				FileID:        fileID,
				Name:          sym.Name,
				QualifiedName: sym.QualifiedName,
				Kind:          string(sym.Kind),
				Language:      sym.Language,
				FilePath:      sym.FilePath,
				StartLine:     sym.StartLine,
				EndLine:       sym.EndLine,
				Signature:     sym.Signature,
				DocComment:    sym.DocComment,
				IsTest:        sym.IsTest,
			}
			s.repoSymbols[repoID] = append(s.repoSymbols[repoID], symID)
			s.fileSymbols[fileID] = append(s.fileSymbols[fileID], symID)

			switch sym.Kind {
			case indexer.SymbolFunction, indexer.SymbolMethod:
				funcCount++
			case indexer.SymbolClass, indexer.SymbolStruct, indexer.SymbolInterface, indexer.SymbolEnum, indexer.SymbolTrait:
				classCount++
			}
		}

		for _, imp := range fr.Imports {
			s.imports = append(s.imports, StoredImport{
				FileID: fileID,
				Path:   imp.Path,
				Line:   imp.Line,
			})
		}
	}

	// Store call graph relations
	for _, rel := range result.Relations {
		if rel.Type != indexer.RelationCalls {
			continue
		}
		callerID := idMap[rel.SourceID]
		calleeID := idMap[rel.TargetID]
		if callerID == "" || calleeID == "" {
			continue
		}
		s.callGraph[callerID] = append(s.callGraph[callerID], calleeID)
		s.reverseCallGraph[calleeID] = append(s.reverseCallGraph[calleeID], callerID)
	}

	// Store modules
	for _, mod := range result.Modules {
		modID := uuid.New().String()
		s.modules[modID] = &StoredModule{
			ID:        modID,
			RepoID:    repoID,
			Name:      mod.Name,
			Path:      mod.Path,
			FileCount: mod.FileCount,
		}
		s.repoModules[repoID] = append(s.repoModules[repoID], modID)
	}

	// Update repository metadata
	now := time.Now()
	repo.FileCount = result.TotalFiles
	repo.FunctionCount = funcCount
	repo.ClassCount = classCount
	repo.LastIndexedAt = now
	repo.Status = "ready"
	repo.IndexError = ""

	return repo, nil
}

// UpdateRepositoryMeta updates mutable metadata fields on a repository.
func (s *Store) UpdateRepositoryMeta(id string, meta RepositoryMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo := s.repos[id]
	if repo == nil {
		return
	}
	if meta.ClonePath != "" {
		repo.ClonePath = meta.ClonePath
	}
	if meta.RemoteURL != "" {
		repo.RemoteURL = meta.RemoteURL
	}
	if meta.CommitSHA != "" {
		repo.CommitSHA = meta.CommitSHA
	}
	if meta.Branch != "" {
		repo.Branch = meta.Branch
	}
	if meta.AuthToken != "" {
		repo.AuthToken = meta.AuthToken
	}
	if meta.GenerationModeDefault != "" {
		repo.GenerationModeDefault = meta.GenerationModeDefault
	}
}

// ListRepositories returns all repositories.
func (s *Store) ListRepositories() []*Repository {
	s.mu.RLock()
	defer s.mu.RUnlock()

	repos := make([]*Repository, 0, len(s.repos))
	for _, r := range s.repos {
		repos = append(repos, r)
	}
	return repos
}

// GetRepository returns a repository by ID.
func (s *Store) GetRepository(id string) *Repository {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.repos[id]
}

// GetRepositoryByPath returns a repository by its path.
func (s *Store) GetRepositoryByPath(path string) *Repository {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.repos {
		if r.Path == path {
			return r
		}
	}
	return nil
}

// RemoveRepository removes a repository and all its data.
func (s *Store) RemoveRepository(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.repos[id]; !exists {
		return false
	}

	// Remove symbols and call graph entries
	for _, symID := range s.repoSymbols[id] {
		// Clean up call graph
		for _, calleeID := range s.callGraph[symID] {
			s.reverseCallGraph[calleeID] = removeFromSlice(s.reverseCallGraph[calleeID], symID)
		}
		delete(s.callGraph, symID)
		for _, callerID := range s.reverseCallGraph[symID] {
			s.callGraph[callerID] = removeFromSlice(s.callGraph[callerID], symID)
		}
		delete(s.reverseCallGraph, symID)

		delete(s.symbols, symID)
	}
	delete(s.repoSymbols, id)

	// Remove files
	for _, fileID := range s.repoFiles[id] {
		delete(s.files, fileID)
		delete(s.fileSymbols, fileID)
	}
	delete(s.repoFiles, id)

	// Remove modules
	for _, modID := range s.repoModules[id] {
		delete(s.modules, modID)
	}
	delete(s.repoModules, id)

	// Remove requirements
	for _, reqID := range s.repoRequirements[id] {
		delete(s.requirements, reqID)
		delete(s.reqLinks, reqID)
	}
	delete(s.repoRequirements, id)

	// Remove links
	for _, linkID := range s.repoLinks[id] {
		link := s.links[linkID]
		if link != nil {
			// Clean up reverse indexes
			s.symLinks[link.SymbolID] = removeFromSlice(s.symLinks[link.SymbolID], linkID)
		}
		delete(s.links, linkID)
	}
	delete(s.repoLinks, id)

	delete(s.repos, id)
	return true
}

// GetFiles returns all files for a repository.
func (s *Store) GetFiles(repoID string) []*File {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var files []*File
	for _, fid := range s.repoFiles[repoID] {
		if f := s.files[fid]; f != nil {
			files = append(files, f)
		}
	}
	return files
}

// GetFilesPaginated returns files for a repository with optional path prefix filtering and pagination.
func (s *Store) GetFilesPaginated(repoID string, pathPrefix *string, limit, offset int) ([]*File, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []*File
	for _, fid := range s.repoFiles[repoID] {
		f := s.files[fid]
		if f == nil {
			continue
		}
		if pathPrefix != nil && *pathPrefix != "" {
			if !strings.HasPrefix(f.Path, *pathPrefix) {
				continue
			}
		}
		all = append(all, f)
	}

	total := len(all)
	if offset >= total {
		return nil, total
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, total
}

// GetSymbols returns symbols for a repository with optional filtering.
func (s *Store) GetSymbols(repoID string, query *string, kind *string, limit, offset int) ([]*StoredSymbol, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []*StoredSymbol
	for _, symID := range s.repoSymbols[repoID] {
		sym := s.symbols[symID]
		if sym == nil {
			continue
		}
		if kind != nil && sym.Kind != *kind {
			continue
		}
		if query != nil && *query != "" {
			q := strings.ToLower(*query)
			if !strings.Contains(strings.ToLower(sym.Name), q) &&
				!strings.Contains(strings.ToLower(sym.QualifiedName), q) {
				continue
			}
		}
		all = append(all, sym)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].FilePath != all[j].FilePath {
			return all[i].FilePath < all[j].FilePath
		}
		return all[i].Name < all[j].Name
	})

	total := len(all)

	if offset >= len(all) {
		return nil, total
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}

	return all, total
}

// GetFileSymbols returns symbols for a specific file.
func (s *Store) GetFileSymbols(fileID string) []*StoredSymbol {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var syms []*StoredSymbol
	for _, symID := range s.fileSymbols[fileID] {
		if sym := s.symbols[symID]; sym != nil {
			syms = append(syms, sym)
		}
	}
	return syms
}

// GetModules returns all modules for a repository.
func (s *Store) GetModules(repoID string) []*StoredModule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var mods []*StoredModule
	for _, modID := range s.repoModules[repoID] {
		if m := s.modules[modID]; m != nil {
			mods = append(mods, m)
		}
	}
	return mods
}

// SearchContent searches file content for a query string.
func (s *Store) SearchContent(repoID, query string, limit int) []SearchResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []SearchResult
	q := strings.ToLower(query)

	// Search symbol names
	for _, symID := range s.repoSymbols[repoID] {
		sym := s.symbols[symID]
		if sym == nil {
			continue
		}
		if strings.Contains(strings.ToLower(sym.Name), q) ||
			strings.Contains(strings.ToLower(sym.QualifiedName), q) {
			results = append(results, SearchResult{
				Type:     "symbol",
				Name:     sym.Name,
				FilePath: sym.FilePath,
				Line:     sym.StartLine,
				Snippet:  sym.Signature,
				Kind:     sym.Kind,
			})
		}
		if limit > 0 && len(results) >= limit {
			break
		}
	}

	// Search file paths
	for _, fileID := range s.repoFiles[repoID] {
		f := s.files[fileID]
		if f == nil {
			continue
		}
		if strings.Contains(strings.ToLower(f.Path), q) {
			results = append(results, SearchResult{
				Type:     "file",
				Name:     f.Path,
				FilePath: f.Path,
			})
		}
		if limit > 0 && len(results) >= limit {
			break
		}
	}

	return results
}

// GetCallers returns the IDs of symbols that call the given symbol.
func (s *Store) GetCallers(symbolID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reverseCallGraph[symbolID]
}

// GetCallees returns the IDs of symbols called by the given symbol.
func (s *Store) GetCallees(symbolID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.callGraph[symbolID]
}

// GetCallEdges returns all call edges for a repository in a single batch.
func (s *Store) GetCallEdges(repoID string) []CallEdge {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build set of symbol IDs belonging to this repo.
	repoSymbols := make(map[string]bool)
	for id, sym := range s.symbols {
		if sym.RepoID == repoID {
			repoSymbols[id] = true
		}
	}

	var edges []CallEdge
	for callerID, callees := range s.callGraph {
		if !repoSymbols[callerID] {
			continue
		}
		for _, calleeID := range callees {
			edges = append(edges, CallEdge{CallerID: callerID, CalleeID: calleeID})
		}
	}
	return edges
}

// GetImports returns all imports for a repository.
func (s *Store) GetImports(repoID string) []*StoredImport {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build set of file IDs for this repo
	fileIDs := make(map[string]bool)
	for _, fid := range s.repoFiles[repoID] {
		fileIDs[fid] = true
	}

	var result []*StoredImport
	for i := range s.imports {
		if fileIDs[s.imports[i].FileID] {
			imp := s.imports[i]
			result = append(result, &imp)
		}
	}
	return result
}

// SearchResult represents a search match.
type SearchResult struct {
	Type     string `json:"type"` // "symbol", "file", "content"
	Name     string `json:"name"`
	FilePath string `json:"file_path"`
	Line     int    `json:"line,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// Stats returns aggregate statistics.
func (s *Store) Stats() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	containsCount := 0
	for _, syms := range s.fileSymbols {
		containsCount += len(syms)
	}

	return map[string]int{
		"repositories": len(s.repos),
		"files":        len(s.files),
		"symbols":      len(s.symbols),
		"modules":      len(s.modules),
		"requirements": len(s.requirements),
		"links":        len(s.links),
		"imports":      len(s.imports),
		"contains":     containsCount,
	}
}

func removeFromSlice(s []string, val string) []string {
	var result []string
	for _, v := range s {
		if v != val {
			result = append(result, v)
		}
	}
	return result
}

// StoreLink adds a requirement-code link.
func (s *Store) StoreLink(repoID string, link *StoredLink) *StoredLink {
	s.mu.Lock()
	defer s.mu.Unlock()

	link.ID = uuid.New().String()
	link.RepoID = repoID
	link.CreatedAt = time.Now()

	s.links[link.ID] = link
	s.repoLinks[repoID] = append(s.repoLinks[repoID], link.ID)
	s.reqLinks[link.RequirementID] = append(s.reqLinks[link.RequirementID], link.ID)
	s.symLinks[link.SymbolID] = append(s.symLinks[link.SymbolID], link.ID)

	return link
}

// StoreLinks adds multiple links at once.
func (s *Store) StoreLinks(repoID string, links []*StoredLink) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, link := range links {
		link.ID = uuid.New().String()
		link.RepoID = repoID
		link.CreatedAt = now
		s.links[link.ID] = link
		s.repoLinks[repoID] = append(s.repoLinks[repoID], link.ID)
		s.reqLinks[link.RequirementID] = append(s.reqLinks[link.RequirementID], link.ID)
		s.symLinks[link.SymbolID] = append(s.symLinks[link.SymbolID], link.ID)
	}
	return len(links)
}

// GetLink returns a link by ID.
func (s *Store) GetLink(id string) *StoredLink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.links[id]
}

// GetLinksForRequirement returns links for a requirement ID.
// If includeRejected is false, rejected links are excluded.
func (s *Store) GetLinksForRequirement(reqID string, includeRejected bool) []*StoredLink {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*StoredLink
	for _, linkID := range s.reqLinks[reqID] {
		link := s.links[linkID]
		if link == nil {
			continue
		}
		if !includeRejected && link.Rejected {
			continue
		}
		result = append(result, link)
	}
	return result
}

// GetLinksForSymbol returns links for a symbol ID.
func (s *Store) GetLinksForSymbol(symID string, includeRejected bool) []*StoredLink {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*StoredLink
	for _, linkID := range s.symLinks[symID] {
		link := s.links[linkID]
		if link == nil {
			continue
		}
		if !includeRejected && link.Rejected {
			continue
		}
		result = append(result, link)
	}
	return result
}

// GetLinksForFile returns links for symbols in a file, optionally filtered by line range.
func (s *Store) GetLinksForFile(fileID string, startLine, endLine int, minConfidence float64) []*StoredLink {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*StoredLink
	for _, symID := range s.fileSymbols[fileID] {
		sym := s.symbols[symID]
		if sym == nil {
			continue
		}

		// Filter by line range if specified
		if startLine > 0 && sym.EndLine < startLine {
			continue
		}
		if endLine > 0 && sym.StartLine > endLine {
			continue
		}

		for _, linkID := range s.symLinks[symID] {
			link := s.links[linkID]
			if link == nil || link.Rejected {
				continue
			}
			if link.Confidence < minConfidence {
				continue
			}
			result = append(result, link)
		}
	}
	return result
}

// VerifyLink marks a link as verified or rejected.
func (s *Store) VerifyLink(linkID string, verified bool, verifiedBy string) *StoredLink {
	s.mu.Lock()
	defer s.mu.Unlock()

	link := s.links[linkID]
	if link == nil {
		return nil
	}

	if verified {
		link.Verified = true
		link.Rejected = false
		link.Confidence = 1.0
	} else {
		link.Rejected = true
		link.Verified = false
	}
	link.VerifiedBy = verifiedBy

	return link
}

// StoreRequirement adds a requirement to the store.
func (s *Store) StoreRequirement(repoID string, req *StoredRequirement) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req.ID = uuid.New().String()
	req.RepoID = repoID
	now := time.Now()
	req.CreatedAt = now
	req.UpdatedAt = now

	s.requirements[req.ID] = req
	s.repoRequirements[repoID] = append(s.repoRequirements[repoID], req.ID)
}

// StoreRequirements adds multiple requirements and returns the count stored.
func (s *Store) StoreRequirements(repoID string, reqs []*StoredRequirement) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	count := 0
	for _, req := range reqs {
		req.ID = uuid.New().String()
		req.RepoID = repoID
		req.CreatedAt = now
		req.UpdatedAt = now

		s.requirements[req.ID] = req
		s.repoRequirements[repoID] = append(s.repoRequirements[repoID], req.ID)
		count++
	}
	return count
}

// GetRequirements returns requirements for a repository with pagination.
func (s *Store) GetRequirements(repoID string, limit, offset int) ([]*StoredRequirement, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	reqIDs := s.repoRequirements[repoID]
	total := len(reqIDs)

	if offset >= total {
		return nil, total
	}

	end := total
	if offset+limit < total && limit > 0 {
		end = offset + limit
	}

	var reqs []*StoredRequirement
	for _, id := range reqIDs[offset:end] {
		if r := s.requirements[id]; r != nil {
			reqs = append(reqs, r)
		}
	}
	return reqs, total
}

// GetRequirement returns a requirement by ID.
func (s *Store) GetRequirement(id string) *StoredRequirement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.requirements[id]
}

// GetRequirementsByIDs returns requirements by a list of IDs in a single lookup.
func (s *Store) GetRequirementsByIDs(ids []string) map[string]*StoredRequirement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*StoredRequirement, len(ids))
	for _, id := range ids {
		if req, ok := s.requirements[id]; ok {
			result[id] = req
		}
	}
	return result
}

// GetRequirementByExternalID returns a requirement by external ID within a repo.
func (s *Store) GetRequirementByExternalID(repoID, externalID string) *StoredRequirement {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, reqID := range s.repoRequirements[repoID] {
		if r := s.requirements[reqID]; r != nil && r.ExternalID == externalID {
			return r
		}
	}
	return nil
}

// GetLinksForRepo returns all non-rejected links for a repository.
func (s *Store) GetLinksForRepo(repoID string) []*StoredLink {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*StoredLink
	for _, linkID := range s.repoLinks[repoID] {
		link := s.links[linkID]
		if link == nil || link.Rejected {
			continue
		}
		result = append(result, link)
	}
	return result
}

// SetRepositoryError marks a repository as having an error.
func (s *Store) SetRepositoryError(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if repo := s.repos[id]; repo != nil {
		repo.Status = "error"
		repo.IndexError = fmt.Sprintf("%v", err)
	}
}

// CacheUnderstandingScore stores the precomputed overall score on the repository.
func (s *Store) CacheUnderstandingScore(id string, overall float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if repo := s.repos[id]; repo != nil {
		repo.UnderstandingScoreVal = &overall
		now := time.Now()
		repo.UnderstandingScoreAt = &now
	}
}

// GetSymbol returns a single symbol by ID.
func (s *Store) GetSymbol(id string) *StoredSymbol {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.symbols[id]
}

// GetSymbolsByIDs returns symbols by a list of IDs in a single lookup.
func (s *Store) GetSymbolsByIDs(ids []string) map[string]*StoredSymbol {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*StoredSymbol, len(ids))
	for _, id := range ids {
		if sym, ok := s.symbols[id]; ok {
			result[id] = sym
		}
	}
	return result
}

// GetSymbolsByFile returns all symbols in a repository for a given file path.
func (s *Store) GetSymbolsByFile(repoID string, filePath string) []*StoredSymbol {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*StoredSymbol
	symIDs := s.repoSymbols[repoID]
	for _, sid := range symIDs {
		sym := s.symbols[sid]
		if sym != nil && sym.FilePath == filePath {
			result = append(result, sym)
		}
	}
	return result
}

// UpdateRequirement updates the priority and tags on an existing requirement.
func (s *Store) UpdateRequirement(id string, priority string, tags []string) *StoredRequirement {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.requirements[id]
	if req == nil {
		return nil
	}
	if priority != "" {
		req.Priority = priority
	}
	if tags != nil {
		req.Tags = tags
	}
	req.UpdatedAt = time.Now()
	return req
}

// UpdateRequirementFields applies a partial update to the requirement.
// Nil fields are preserved; externalId uniqueness is enforced per-repo.
func (s *Store) UpdateRequirementFields(id string, fields RequirementUpdate) *StoredRequirement {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.requirements[id]
	if req == nil {
		return nil
	}
	if fields.ExternalID != nil && *fields.ExternalID != "" && *fields.ExternalID != req.ExternalID {
		// Uniqueness check — reject if another requirement in this repo
		// already owns the external id.
		for otherID := range s.requirements {
			other := s.requirements[otherID]
			if other == nil || otherID == id {
				continue
			}
			if other.RepoID == req.RepoID && other.ExternalID == *fields.ExternalID {
				return nil
			}
		}
		req.ExternalID = *fields.ExternalID
	}
	if fields.Title != nil {
		req.Title = *fields.Title
	}
	if fields.Description != nil {
		req.Description = *fields.Description
	}
	if fields.Priority != nil {
		req.Priority = *fields.Priority
	}
	if fields.Source != nil {
		req.Source = *fields.Source
	}
	if fields.Tags != nil {
		req.Tags = *fields.Tags
	}
	if fields.AcceptanceCriteria != nil {
		req.AcceptanceCriteria = *fields.AcceptanceCriteria
	}
	req.UpdatedAt = time.Now()
	return req
}

// ---------------------------------------------------------------------------
// LLM Usage tracking
// ---------------------------------------------------------------------------

// StoreLLMUsage records an LLM API call.
func (s *Store) StoreLLMUsage(record *LLMUsageRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record.ID = uuid.New().String()
	record.CreatedAt = time.Now()
	s.llmUsage = append(s.llmUsage, *record)
}

// GetLLMUsage returns all LLM usage records, optionally filtered by repoID.
func (s *Store) GetLLMUsage(repoID string, limit int) []LLMUsageRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var results []LLMUsageRecord
	for i := len(s.llmUsage) - 1; i >= 0; i-- {
		r := s.llmUsage[i]
		if repoID != "" && r.RepoID != repoID {
			continue
		}
		results = append(results, r)
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results
}

// ---------------------------------------------------------------------------
// Embedding cache
// ---------------------------------------------------------------------------

// StoreEmbedding caches an embedding vector.
func (s *Store) StoreEmbedding(record *EmbeddingRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record.ID = uuid.New().String()
	record.CreatedAt = time.Now()
	s.embeddings[record.TargetID] = record
}

// GetEmbedding retrieves a cached embedding by target ID.
func (s *Store) GetEmbedding(targetID string) *EmbeddingRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.embeddings[targetID]
}

// ---------------------------------------------------------------------------
// Review results
// ---------------------------------------------------------------------------

// StoreReviewResult persists an AI code review result.
func (s *Store) StoreReviewResult(record *ReviewResultRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record.ID = uuid.New().String()
	record.CreatedAt = time.Now()
	s.reviewResults[record.ID] = record
}

// GetReviewResults returns review results for a target (symbol or file).
func (s *Store) GetReviewResults(targetID string) []*ReviewResultRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var results []*ReviewResultRecord
	for _, r := range s.reviewResults {
		if r.TargetID == targetID {
			results = append(results, r)
		}
	}
	return results
}

// GetReviewResultsForRepo returns all review results for a given repository.
func (s *Store) GetReviewResultsForRepo(repoID string) []*ReviewResultRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var results []*ReviewResultRecord
	for _, r := range s.reviewResults {
		if r.RepoID == repoID {
			results = append(results, r)
		}
	}
	return results
}

// GetPublicSymbolDocCoverage returns the count of public symbols with doc comments
// and the total count of public symbols for a repository.
func (s *Store) GetPublicSymbolDocCoverage(repoID string) (withDocs int, total int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, symID := range s.repoSymbols[repoID] {
		sym := s.symbols[symID]
		if sym == nil || !IsPublicSymbol(sym) {
			continue
		}
		total++
		if strings.TrimSpace(sym.DocComment) != "" {
			withDocs++
		}
	}
	return
}

// GetTestSymbolRatio returns the count of test symbols and total symbols for a repository.
func (s *Store) GetTestSymbolRatio(repoID string) (tests int, total int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, symID := range s.repoSymbols[repoID] {
		sym := s.symbols[symID]
		if sym == nil {
			continue
		}
		total++
		if sym.IsTest {
			tests++
		}
	}
	return
}

// GetAICodeFileRatio returns the count of AI-generated files (AIScore > 0.5) and total files.
func (s *Store) GetAICodeFileRatio(repoID string) (aiFiles int, totalFiles int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, fid := range s.repoFiles[repoID] {
		f := s.files[fid]
		if f == nil {
			continue
		}
		totalFiles++
		if f.AIScore > 0.5 {
			aiFiles++
		}
	}
	return
}

// StoreImpactReport stores an impact report for a repository.
func (s *Store) StoreImpactReport(repoID string, report *ImpactReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.impactReports[repoID] = append(s.impactReports[repoID], report)
}

// GetLatestImpactReport returns the most recent impact report for a repository.
func (s *Store) GetLatestImpactReport(repoID string) *ImpactReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reports := s.impactReports[repoID]
	if len(reports) == 0 {
		return nil
	}
	return reports[len(reports)-1]
}

// GetImpactReports returns impact reports for a repository, most recent first.
func (s *Store) GetImpactReports(repoID string, limit int) ([]*ImpactReport, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reports := s.impactReports[repoID]
	total := len(reports)
	if total == 0 {
		return nil, 0
	}

	// Return most recent first
	if limit <= 0 || limit > total {
		limit = total
	}
	result := make([]*ImpactReport, limit)
	for i := 0; i < limit; i++ {
		result[i] = reports[total-1-i]
	}
	return result, total
}

// ---------------------------------------------------------------------------
// Discovered Requirement operations (spec extraction)
// ---------------------------------------------------------------------------

// StoreDiscoveredRequirement adds a single discovered requirement.
func (s *Store) StoreDiscoveredRequirement(repoID string, req *DiscoveredRequirement) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req.ID = uuid.New().String()
	req.RepoID = repoID
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	if req.Status == "" {
		req.Status = "discovered"
	}

	s.discoveredRequirements[req.ID] = req
	s.repoDiscoveredReqs[repoID] = append(s.repoDiscoveredReqs[repoID], req.ID)
}

// StoreDiscoveredRequirements adds multiple discovered requirements and returns the count stored.
func (s *Store) StoreDiscoveredRequirements(repoID string, reqs []*DiscoveredRequirement) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	count := 0
	for _, req := range reqs {
		req.ID = uuid.New().String()
		req.RepoID = repoID
		if req.CreatedAt.IsZero() {
			req.CreatedAt = now
		}
		if req.Status == "" {
			req.Status = "discovered"
		}

		s.discoveredRequirements[req.ID] = req
		s.repoDiscoveredReqs[repoID] = append(s.repoDiscoveredReqs[repoID], req.ID)
		count++
	}
	return count
}

// GetDiscoveredRequirements returns discovered requirements with optional filters and pagination.
func (s *Store) GetDiscoveredRequirements(repoID string, status *string, confidence *string, limit, offset int) ([]*DiscoveredRequirement, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := s.repoDiscoveredReqs[repoID]
	var filtered []*DiscoveredRequirement
	for _, id := range ids {
		req := s.discoveredRequirements[id]
		if req == nil {
			continue
		}
		if status != nil && req.Status != *status {
			continue
		}
		if confidence != nil && req.Confidence != *confidence {
			continue
		}
		filtered = append(filtered, req)
	}

	// Sort by confidence (high first), then by creation time (newest first)
	sort.Slice(filtered, func(i, j int) bool {
		ci := confidenceRank(filtered[i].Confidence)
		cj := confidenceRank(filtered[j].Confidence)
		if ci != cj {
			return ci > cj
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	total := len(filtered)
	if offset >= total {
		return nil, total
	}
	filtered = filtered[offset:]
	if limit > 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}
	return filtered, total
}

func confidenceRank(c string) int {
	switch c {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// GetDiscoveredRequirement returns a single discovered requirement by ID.
func (s *Store) GetDiscoveredRequirement(id string) *DiscoveredRequirement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.discoveredRequirements[id]
}

// PromoteDiscoveredRequirement marks a discovered requirement as promoted.
func (s *Store) PromoteDiscoveredRequirement(id string, requirementID string) *DiscoveredRequirement {
	s.mu.Lock()
	defer s.mu.Unlock()

	req := s.discoveredRequirements[id]
	if req == nil {
		return nil
	}
	req.Status = "promoted"
	req.PromotedTo = requirementID
	req.PromotedAt = time.Now()
	return req
}

// DismissDiscoveredRequirement marks a discovered requirement as dismissed.
func (s *Store) DismissDiscoveredRequirement(id string, dismissedBy string, reason string) *DiscoveredRequirement {
	s.mu.Lock()
	defer s.mu.Unlock()

	req := s.discoveredRequirements[id]
	if req == nil {
		return nil
	}
	req.Status = "dismissed"
	req.DismissedBy = dismissedBy
	req.DismissedReason = reason
	req.DismissedAt = time.Now()
	return req
}

// DeleteDiscoveredRequirementsByRepo removes all discovered requirements for a repo.
// Only deletes requirements with status "discovered"; promoted/dismissed are preserved.
func (s *Store) DeleteDiscoveredRequirementsByRepo(repoID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := s.repoDiscoveredReqs[repoID]
	deleted := 0
	var remaining []string
	for _, id := range ids {
		req := s.discoveredRequirements[id]
		if req != nil && req.Status == "discovered" {
			delete(s.discoveredRequirements, id)
			deleted++
		} else {
			remaining = append(remaining, id)
		}
	}
	s.repoDiscoveredReqs[repoID] = remaining
	return deleted
}

// --- Cross-Repo Federation (in-memory stubs) ---

func (s *Store) LinkRepos(sourceRepoID, targetRepoID string) (*RepoLink, error) {
	return nil, fmt.Errorf("federation not supported in in-memory store")
}
func (s *Store) UnlinkRepos(linkID string) error {
	return fmt.Errorf("federation not supported in in-memory store")
}
func (s *Store) GetRepoLinks(repoID string) ([]*RepoLink, error) {
	return nil, nil
}
func (s *Store) StoreCrossRepoRef(ref *CrossRepoRef) error {
	return fmt.Errorf("federation not supported in in-memory store")
}
func (s *Store) StoreCrossRepoRefs(refs []*CrossRepoRef) int {
	return 0
}
func (s *Store) GetCrossRepoRefs(repoID string, refType *string, limit int) ([]*CrossRepoRef, error) {
	return nil, nil
}
func (s *Store) GetSymbolCrossRepoRefs(symbolID string) ([]*CrossRepoRef, error) {
	return nil, nil
}
func (s *Store) DeleteCrossRepoRefsForRepo(repoID string) error {
	return nil
}
func (s *Store) DeleteCrossRepoRefsBetweenRepos(repoA, repoB string) error {
	return nil
}
func (s *Store) StoreAPIContract(contract *APIContract) error {
	return fmt.Errorf("federation not supported in in-memory store")
}
func (s *Store) GetAPIContracts(repoID string) ([]*APIContract, error) {
	return nil, nil
}
func (s *Store) DeleteAPIContractsForRepo(repoID string) error {
	return nil
}
