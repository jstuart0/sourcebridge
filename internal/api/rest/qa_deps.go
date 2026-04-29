// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/search"
)

// qaJobOpFromType picks the resolution.Op for a QA job_type. The QA
// pipeline has multiple sub-ops (classify / decompose / synth / agent)
// that can use different provider configs in advanced mode; mirroring
// the per-op resolution lets metrics attribute work correctly.
func qaJobOpFromType(jobType string) string {
	switch jobType {
	case "qa.classify":
		return resolution.OpQAClassify
	case "qa.decompose":
		return resolution.OpQADecompose
	case "qa.deep_synth":
		return resolution.OpQADeepSynth
	case "qa.agent_turn":
		return resolution.OpQAAgentTurn
	}
	return resolution.OpQASynth
}

// qaRepoLocator adapts the graph store's Repository records to
// qa.RepoLocator. Uses the same clone-path-resolution logic as the
// GraphQL resolvers (resolveRepoSourcePath in api/graphql/helpers.go)
// so QA sees exactly the paths the rest of the product sees.
type qaRepoLocator struct {
	store         graphstore.GraphStore
	repoCacheBase string
}

func newQARepoLocator(store graphstore.GraphStore, repoCacheBase string) *qaRepoLocator {
	return &qaRepoLocator{store: store, repoCacheBase: repoCacheBase}
}

// LocateRepoClone resolves a repo ID to its on-disk root. Mirrors
// resolveRepoSourcePath's decision order: persisted clone_path →
// computed cache path → local Path fallback.
func (l *qaRepoLocator) LocateRepoClone(repoID string) (string, bool) {
	if l == nil || l.store == nil {
		return "", false
	}
	repo := l.store.GetRepository(repoID)
	if repo == nil {
		return "", false
	}
	if repo.ClonePath != "" {
		if info, err := os.Stat(repo.ClonePath); err == nil && info.IsDir() {
			return repo.ClonePath, true
		}
	}
	if repo.Name != "" && l.repoCacheBase != "" {
		computed := filepath.Join(l.repoCacheBase, "repos", sanitizeRepoNameForQA(repo.Name))
		if info, err := os.Stat(computed); err == nil && info.IsDir() {
			return computed, true
		}
	}
	if repo.Path != "" {
		if info, err := os.Stat(repo.Path); err == nil && info.IsDir() {
			return repo.Path, true
		}
	}
	return "", false
}

// sanitizeRepoNameForQA mirrors sanitizeRepoName in
// internal/api/graphql/helpers.go. Duplicated here rather than
// exported because the other variant is unexported and changing its
// visibility would leak package internals unnecessarily — the rule
// is intentionally narrow (replace / and : with -).
func sanitizeRepoNameForQA(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch r {
		case '/', ':':
			out = append(out, '-')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// qaGraphLookup adapts the graph store's symbol lookup to
// qa.graphSymbolLookup without leaking graph.StoredSymbol into the qa
// package.
type qaGraphLookup struct {
	store graphstore.GraphStore
}

func (g *qaGraphLookup) Lookup(id string) (string, string, string, int, int, bool) {
	if g == nil || g.store == nil {
		return "", "", "", 0, 0, false
	}
	sym := g.store.GetSymbol(id)
	if sym == nil {
		return "", "", "", 0, 0, false
	}
	qn := sym.QualifiedName
	if qn == "" {
		qn = sym.Name
	}
	return qn, sym.FilePath, sym.Language, sym.StartLine, sym.EndLine, true
}

// qaGraphAdapter adapts the store's caller/callee methods. Returns a
// minimal-surface value that qa.NewGraphExpander consumes.
type qaGraphAdapter struct {
	store graphstore.GraphStore
}

func (a *qaGraphAdapter) GetCallers(id string) []string { return a.store.GetCallers(id) }
func (a *qaGraphAdapter) GetCallees(id string) []string { return a.store.GetCallees(id) }

// qaArtifactLookup adapts the knowledge store to qa.ArtifactLookup.
// Returns the same context block the legacy discussCode resolver
// built so F10/F11 shape preservation doesn't regress.
type qaArtifactLookup struct {
	store knowledge.KnowledgeStore
}

func (a *qaArtifactLookup) ArtifactContext(id string) string {
	if a == nil || a.store == nil || id == "" {
		return ""
	}
	art := a.store.GetKnowledgeArtifact(id)
	if art == nil {
		return ""
	}
	return discussionContextFromArtifactQA(art)
}

// discussionContextFromArtifactQA duplicates the helper in
// internal/api/graphql/helpers.go so we don't import the graphql
// package from rest (which would be a layering inversion). If the
// legacy helper changes, keep this in sync.
func discussionContextFromArtifactQA(artifact *knowledge.Artifact) string {
	if artifact == nil || len(artifact.Sections) == 0 {
		return ""
	}
	scopePath := "repository"
	if artifact.Scope != nil {
		scopePath = artifact.Scope.ScopePath
	}
	parts := []string{
		fmt.Sprintf("Indexed %s context for %s.", lower(string(artifact.Type)), scopePath),
	}
	for idx, section := range artifact.Sections {
		if idx >= 6 {
			break
		}
		body := section.Summary
		if body == "" {
			body = section.Content
		}
		body = trim(body)
		if len(body) > 500 {
			body = body[:500] + "..."
		}
		parts = append(parts, fmt.Sprintf("- %s: %s", section.Title, body))
	}
	return joinLines(parts)
}

// qaRequirementLookup adapts the graph store for requirement
// resolution. One struct implements both RequirementContext (by ID)
// and RequirementLabelsForSymbols (via links) so the orchestrator's
// dependency list stays short.
type qaRequirementLookup struct {
	store graphstore.GraphStore
}

func (r *qaRequirementLookup) RequirementContext(id string) string {
	if r == nil || r.store == nil || id == "" {
		return ""
	}
	req := r.store.GetRequirement(id)
	if req == nil {
		return ""
	}
	return fmt.Sprintf(
		"Requirement context:\nID: %s\nTitle: %s\nDescription: %s",
		req.ExternalID, req.Title, req.Description,
	)
}

func (r *qaRequirementLookup) RequirementLabelsForSymbols(symbolIDs []string) []string {
	if r == nil || r.store == nil || len(symbolIDs) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, sid := range symbolIDs {
		for _, link := range r.store.GetLinksForSymbol(sid, false) {
			if _, dup := seen[link.RequirementID]; dup {
				continue
			}
			seen[link.RequirementID] = struct{}{}
			req := r.store.GetRequirement(link.RequirementID)
			if req == nil {
				continue
			}
			label := req.ExternalID
			if label == "" {
				label = req.Title
			}
			if label != "" {
				out = append(out, label)
			}
		}
	}
	return out
}

// qaSymbolLookup resolves symbol IDs + files to context blocks. Uses
// the same metadata fields as the legacy resolver so the synthesis
// prompt sees identical text.
type qaSymbolLookup struct {
	store graphstore.GraphStore
}

func (s *qaSymbolLookup) SymbolContext(id string) string {
	if s == nil || s.store == nil || id == "" {
		return ""
	}
	sym := s.store.GetSymbol(id)
	if sym == nil {
		return ""
	}
	parts := []string{"Indexed symbol: " + sym.QualifiedName}
	if sym.Signature != "" {
		parts = append(parts, sym.Signature)
	}
	if sym.DocComment != "" {
		parts = append(parts, sym.DocComment)
	}
	return joinLines(parts)
}

func (s *qaSymbolLookup) SymbolFilePath(id string) string {
	if s == nil || s.store == nil || id == "" {
		return ""
	}
	sym := s.store.GetSymbol(id)
	if sym == nil {
		return ""
	}
	return sym.FilePath
}

func (s *qaSymbolLookup) SymbolsInFile(repoID, filePath string) []qa.SymbolContextRef {
	if s == nil || s.store == nil || repoID == "" || filePath == "" {
		return nil
	}
	syms := s.store.GetSymbolsByFile(repoID, filePath)
	out := make([]qa.SymbolContextRef, 0, len(syms))
	for _, sym := range syms {
		out = append(out, qa.SymbolContextRef{
			ID:            sym.ID,
			Name:          sym.Name,
			QualifiedName: sym.QualifiedName,
		})
	}
	return out
}

// qaFileReader reads files from repo clones via the shared locator
// and path-traversal-safe join. Implements qa.FileReader.
type qaFileReader struct {
	locator *qaRepoLocator
}

func (r *qaFileReader) ReadRepoFile(repoID, filePath string) (string, error) {
	if r == nil || r.locator == nil {
		return "", errNoLocator
	}
	root, ok := r.locator.LocateRepoClone(repoID)
	if !ok || root == "" {
		return "", errRepoUnavailable
	}
	abs, err := safeJoinRepoPath(root, filePath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// safeJoinRepoPath duplicates safeJoinPath from internal/api/graphql
// for the same layering reason as the artifact helper above. Rejects
// absolute paths and any join that escapes the repo root.
func safeJoinRepoPath(repoRoot, relPath string) (string, error) {
	relPath = trimPrefix(relPath, "./")
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute path not allowed: %s", relPath)
	}
	joined := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolving repo root: %w", err)
	}
	if absJoined != absRoot && !hasPrefix(absJoined, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal rejected: %s", relPath)
	}
	return absJoined, nil
}

// local helpers to avoid pulling strings just for these calls; a
// separate strings-based impl would be identical.
func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
func trim(s string) string {
	start, end := 0, len(s)
	for start < end {
		c := s[start]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			start++
			continue
		}
		break
	}
	for end > start {
		c := s[end-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			end--
			continue
		}
		break
	}
	return s[start:end]
}
func joinLines(ps []string) string {
	var total int
	for _, p := range ps {
		total += len(p) + 1
	}
	b := make([]byte, 0, total)
	for i, p := range ps {
		if i > 0 {
			b = append(b, '\n')
		}
		b = append(b, p...)
	}
	return string(b)
}
func trimPrefix(s, pfx string) string {
	if len(s) >= len(pfx) && s[:len(pfx)] == pfx {
		return s[len(pfx):]
	}
	return s
}
func hasPrefix(s, pfx string) bool {
	return len(s) >= len(pfx) && s[:len(pfx)] == pfx
}

// qaSearcher adapts the hybrid retrieval service to qa.Searcher. The
// bridge is narrow on purpose — internal/qa doesn't know about
// search.Request/Response shapes.
type qaSearcher struct {
	svc *search.Service
}

func (q *qaSearcher) SearchForQA(ctx context.Context, repoID, query string, limit int) ([]qa.SearchHit, error) {
	if q == nil || q.svc == nil {
		return nil, nil
	}
	resp, err := q.svc.Search(ctx, &search.Request{
		Repo:  repoID,
		Query: query,
		Limit: limit,
		Mode:  "deep",
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	out := make([]qa.SearchHit, 0, len(resp.Results))
	for _, r := range resp.Results {
		hit := qa.SearchHit{
			EntityType: r.EntityType,
			EntityID:   r.EntityID,
			Title:      r.Title,
			Subtitle:   r.Subtitle,
			FilePath:   r.FilePath,
			StartLine:  r.Line,
			Score:      r.Score,
			Signals:    r.Signals.Fired(),
		}
		// Symbol end-line lives on the stored symbol; pull it through
		// when present so the deep pipeline gets the real function
		// span, not just the start.
		if r.Symbol != nil {
			if hit.StartLine == 0 {
				hit.StartLine = r.Symbol.StartLine
			}
			hit.EndLine = r.Symbol.EndLine
		}
		out = append(out, hit)
	}
	return out, nil
}

// qaJobRunner integrates QA synthesis with the LLM job orchestrator
// so Monitor sees qa.* jobs alongside knowledge / reasoning. When the
// orchestrator is nil (tests), callers run inline via the qa.JobRunner
// nil-check.
//
// R3 slice 3: optionally holds an llmResolver so each enqueue can stamp
// the resolved provider on the job (Monitor + per-provider metrics).
// When nil, llm_provider lands as empty.
type qaJobRunner struct {
	orch        *orchestrator.Orchestrator
	llmResolver resolution.Resolver
}

func (j *qaJobRunner) RunSyncQAJob(ctx context.Context, jobType, targetKey, repoID string, run func(rt qa.TokenReporter) error) error {
	if j == nil || j.orch == nil {
		return run(nil)
	}
	provider := ""
	if j.llmResolver != nil {
		op := qaJobOpFromType(jobType)
		if snap, err := j.llmResolver.Resolve(ctx, repoID, op); err == nil {
			provider = snap.Provider
		}
	}
	job, err := j.orch.EnqueueSync(ctx, &llm.EnqueueRequest{
		Subsystem:   llm.SubsystemQA,
		JobType:     jobType,
		TargetKey:   targetKey,
		RepoID:      repoID,
		LLMProvider: provider,
		Run: func(rt llm.Runtime) error {
			return run(rt)
		},
	})
	if err != nil {
		return err
	}
	if job != nil && job.Status == llm.StatusFailed {
		if job.ErrorMessage != "" {
			return errors.New(job.ErrorMessage)
		}
		return errors.New("qa job failed")
	}
	return nil
}

// compile-time check: adapters satisfy the qa package's interfaces.
// This catches drift if the qa interfaces change.
var _ qa.RepoLocator = (*qaRepoLocator)(nil)
var _ qa.ArtifactLookup = (*qaArtifactLookup)(nil)
var _ qa.RequirementLookup = (*qaRequirementLookup)(nil)
var _ qa.SymbolLookup = (*qaSymbolLookup)(nil)
var _ qa.FileReader = (*qaFileReader)(nil)
var _ qa.JobRunner = (*qaJobRunner)(nil)
var _ qa.Searcher = (*qaSearcher)(nil)

// sentinel errors for the file reader. Kept internal — callers see
// these via the qa.FileReader return and only need to know the file
// wasn't readable, not the specific cause.
var (
	errNoLocator       = errorString("qa: no repo locator configured")
	errRepoUnavailable = errorString("qa: repo clone unavailable")
)

type errorString string

func (e errorString) Error() string { return string(e) }
