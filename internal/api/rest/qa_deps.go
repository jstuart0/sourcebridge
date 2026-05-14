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
	"github.com/sourcebridge/sourcebridge/internal/indexing/pathutil"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/search"
)

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
func (l *qaRepoLocator) LocateRepoClone(ctx context.Context, repoID string) (string, bool) {
	if l == nil || l.store == nil {
		return "", false
	}
	repo := l.store.GetRepository(ctx, repoID)
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

// sanitizeRepoNameForQA returns a repo name suitable for use in the QA fallback
// cache path. Uses QALegacyPolicy (replace only '/' and ':' with '-', preserve
// everything else) to match the pre-Slice-7 behavior so existing on-disk cache
// directories remain resolvable. Do NOT switch to StrictPolicy here: the strict
// policy drops non-alphanumeric chars (e.g. "my:project" → "myproject") and
// would silently break lookups for repos whose names contain punctuation.
func sanitizeRepoNameForQA(name string) string {
	return pathutil.SanitizeRepoName(name, pathutil.QALegacyPolicy)
}

// qaGraphLookup adapts the graph store's symbol lookup to
// qa.graphSymbolLookup without leaking graph.StoredSymbol into the qa
// package.
type qaGraphLookup struct {
	store graphstore.GraphStore
}

func (g *qaGraphLookup) Lookup(ctx context.Context, id string) (string, string, string, int, int, bool) {
	if g == nil || g.store == nil {
		return "", "", "", 0, 0, false
	}
	sym := g.store.GetSymbol(ctx, id)
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

func (a *qaGraphAdapter) GetCallers(ctx context.Context, id string) []string {
	return a.store.GetCallers(ctx, id)
}
func (a *qaGraphAdapter) GetCallees(ctx context.Context, id string) []string {
	return a.store.GetCallees(ctx, id)
}

// qaArtifactLookup adapts the knowledge store to qa.ArtifactLookup.
// Returns the same context block the legacy discussCode resolver
// built so F10/F11 shape preservation doesn't regress.
type qaArtifactLookup struct {
	store knowledge.KnowledgeStore
}

func (a *qaArtifactLookup) ArtifactContext(ctx context.Context, id string) string {
	if a == nil || a.store == nil || id == "" {
		return ""
	}
	art := a.store.GetKnowledgeArtifact(ctx, id)
	if art == nil {
		return ""
	}
	return discussionContextFromArtifactQA(art)
}

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

func (r *qaRequirementLookup) RequirementContext(ctx context.Context, id string) string {
	if r == nil || r.store == nil || id == "" {
		return ""
	}
	req := r.store.GetRequirement(ctx, id)
	if req == nil {
		return ""
	}
	return fmt.Sprintf(
		"Requirement context:\nID: %s\nTitle: %s\nDescription: %s",
		req.ExternalID, req.Title, req.Description,
	)
}

func (r *qaRequirementLookup) RequirementLabelsForSymbols(ctx context.Context, symbolIDs []string) []string {
	if r == nil || r.store == nil || len(symbolIDs) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, sid := range symbolIDs {
		for _, link := range r.store.GetLinksForSymbol(ctx, sid, false) {
			if _, dup := seen[link.RequirementID]; dup {
				continue
			}
			seen[link.RequirementID] = struct{}{}
			req := r.store.GetRequirement(ctx, link.RequirementID)
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

func (s *qaSymbolLookup) SymbolContext(ctx context.Context, id string) string {
	if s == nil || s.store == nil || id == "" {
		return ""
	}
	sym := s.store.GetSymbol(ctx, id)
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

func (s *qaSymbolLookup) SymbolFilePath(ctx context.Context, id string) string {
	if s == nil || s.store == nil || id == "" {
		return ""
	}
	sym := s.store.GetSymbol(ctx, id)
	if sym == nil {
		return ""
	}
	return sym.FilePath
}

func (s *qaSymbolLookup) SymbolDetails(ctx context.Context, id string) (qa.SymbolDetail, bool) {
	if s == nil || s.store == nil || id == "" {
		return qa.SymbolDetail{}, false
	}
	sym := s.store.GetSymbol(ctx, id)
	if sym == nil {
		return qa.SymbolDetail{}, false
	}
	// Always populate identity from StoredSymbol so callers can
	// render a non-blank label even when ok=false (Decision 5 / plan).
	detail := qa.SymbolDetail{
		ID:            sym.ID,
		Name:          sym.Name,
		QualifiedName: sym.QualifiedName,
		Kind:          sym.Kind,
		Language:      sym.Language,
		DocComment:    sym.DocComment,
	}
	// Decision 5: ok=true means "safe to attempt source slicing."
	// Gate on all three validity conditions.
	if sym.FilePath == "" || sym.StartLine <= 0 || sym.EndLine < sym.StartLine {
		return detail, false
	}
	detail.FilePath = sym.FilePath
	detail.StartLine = sym.StartLine
	detail.EndLine = sym.EndLine
	detail.Signature = sym.Signature
	return detail, true
}

func (s *qaSymbolLookup) SymbolsInFile(ctx context.Context, repoID, filePath string) []qa.SymbolContextRef {
	if s == nil || s.store == nil || repoID == "" || filePath == "" {
		return nil
	}
	syms := s.store.GetSymbolsByFile(ctx, repoID, filePath)
	out := make([]qa.SymbolContextRef, 0, len(syms))
	for _, sym := range syms {
		out = append(out, qa.SymbolContextRef{
			ID:            sym.ID,
			Name:          sym.Name,
			QualifiedName: sym.QualifiedName,
			FilePath:      sym.FilePath,
			StartLine:     sym.StartLine,
			EndLine:       sym.EndLine,
			Signature:     sym.Signature,
		})
	}
	return out
}

// qaFileReader reads files from repo clones via the shared locator
// and path-traversal-safe join. Implements qa.FileReader.
type qaFileReader struct {
	locator *qaRepoLocator
}

func (r *qaFileReader) ReadRepoFile(ctx context.Context, repoID, filePath string) (string, error) {
	if r == nil || r.locator == nil {
		return "", errNoLocator
	}
	root, ok := r.locator.LocateRepoClone(ctx, repoID)
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

// safeJoinRepoPath rejects absolute paths and any join that escapes the repo
// root. Delegates to pathutil.SafeJoinRepoPath.
func safeJoinRepoPath(repoRoot, relPath string) (string, error) {
	return pathutil.SafeJoinRepoPath(repoRoot, relPath)
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
		op := qa.JobTypeToOp(jobType)
		if snap, err := j.llmResolver.Resolve(ctx, repoID, op); err == nil {
			provider = snap.Provider
		}
	}
	// CA-326: pin QA synthesis jobs to a single attempt. The orchestrator's
	// default policy treats DeadlineExceeded as retryable (one retry, so
	// MaxAttempts=2), which on a hung LLM provider doubles the user's wait
	// without higher success probability. A QA synth that burned the full
	// Config.QA.SynthesisTimeoutSecs ceiling once will almost certainly
	// burn it again — the right response is to fail fast and surface the
	// upstream-provider issue. Knowledge-generation jobs keep MaxAttempts=2
	// because long-running jobs hitting timeout once might still complete
	// on a subsequent attempt (e.g. cold-start model swap finishing).
	job, err := j.orch.EnqueueSync(ctx, &llm.EnqueueRequest{
		Subsystem:   llm.SubsystemQA,
		JobType:     jobType,
		TargetKey:   targetKey,
		RepoID:      repoID,
		LLMProvider: provider,
		MaxAttempts: 1,
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
