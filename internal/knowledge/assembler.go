// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// Assembler builds a KnowledgeSnapshot from a repository's indexed data.
// It depends only on the GraphStore interface, keeping it testable with the
// in-memory store.
type Assembler struct {
	store graph.GraphStore
}

// NewAssembler creates an Assembler backed by the given store.
func NewAssembler(store graph.GraphStore) *Assembler {
	return &Assembler{store: store}
}

// Assemble builds a complete KnowledgeSnapshot for the given repository.
// repoSourcePath is the filesystem path to the repository's working tree,
// used for docs discovery.
func (a *Assembler) Assemble(repoID string, repoSourcePath string) (*KnowledgeSnapshot, error) {
	repo := a.store.GetRepository(repoID)
	if repo == nil {
		return nil, fmt.Errorf("repository %s not found", repoID)
	}

	snap := &KnowledgeSnapshot{
		RepositoryID:   repo.ID,
		RepositoryName: repo.Name,
		SourceRevision: buildSourceRevision(repo),
	}

	// Load symbols once and share across sub-assemblers.
	allSymbols, _ := a.store.GetSymbols(repoID, nil, nil, 0, 0)

	a.assembleStructureFromSymbols(snap, repoID, allSymbols)
	a.assembleSymbolsFromSlice(snap, allSymbols)
	a.assembleCallGraphBatch(snap, repoID, allSymbols)
	a.assembleRequirements(snap, repoID)
	a.assembleDocs(snap, repoSourcePath)

	return snap, nil
}

// AssembleScoped builds a narrower snapshot for a module/file/symbol/requirement scope.
func (a *Assembler) AssembleScoped(repoID string, repoSourcePath string, scope ArtifactScope) (*KnowledgeSnapshot, error) {
	scope = scope.Normalize()
	if scope.ScopeType == ScopeRepository {
		return a.Assemble(repoID, repoSourcePath)
	}
	if scope.ScopeType == ScopeRequirement {
		return a.AssembleRequirement(repoID, repoSourcePath, scope)
	}

	full, err := a.Assemble(repoID, repoSourcePath)
	if err != nil {
		return nil, err
	}

	filtered := *full
	filtered.Modules = nil
	filtered.TestCount = 0
	filtered.EntryPoints = nil
	filtered.PublicAPI = nil
	filtered.TestSymbols = nil
	filtered.ComplexSymbols = nil
	filtered.HighFanOutSymbols = nil
	filtered.HighFanInSymbols = nil
	filtered.Requirements = nil
	filtered.Links = nil

	fileByPath := make(map[string]*graph.File)
	for _, file := range a.store.GetFiles(repoID) {
		fileByPath[file.Path] = file
	}

	allowedFiles := a.allowedFilesForScope(repoID, scope)
	allowedSymbols := a.allowedSymbolsForScope(repoID, scope, allowedFiles)

	filtered.FileCount = len(allowedFiles)
	filtered.SymbolCount = len(allowedSymbols)
	for _, mod := range full.Modules {
		if scope.ScopeType == ScopeModule {
			if mod.Path == scope.ScopePath || strings.HasPrefix(mod.Path+"/", scope.ScopePath+"/") {
				filtered.Modules = append(filtered.Modules, mod)
			}
			continue
		}
		if scope.ScopeType == ScopeFile || scope.ScopeType == ScopeSymbol {
			if mod.Path == modulePathForFile(scope.FilePath) {
				filtered.Modules = append(filtered.Modules, mod)
			}
		}
	}

	allowedSet := make(map[string]bool, len(allowedSymbols))
	for _, sym := range allowedSymbols {
		allowedSet[sym.ID] = true
		if sym.IsTest {
			filtered.TestCount++
		}
		ref := SymbolRef{
			ID:            sym.ID,
			Name:          sym.Name,
			QualifiedName: sym.QualifiedName,
			Kind:          sym.Kind,
			FilePath:      sym.FilePath,
			StartLine:     sym.StartLine,
			EndLine:       sym.EndLine,
			DocComment:    sym.DocComment,
			LineCount:     sym.EndLine - sym.StartLine + 1,
		}
		if isEntryPoint(sym) {
			filtered.EntryPoints = append(filtered.EntryPoints, ref)
		}
		if isPublicAPI(sym) {
			filtered.PublicAPI = append(filtered.PublicAPI, ref)
		}
		filtered.ComplexSymbols = append(filtered.ComplexSymbols, ref)
	}

	for _, sym := range full.HighFanOutSymbols {
		if allowedSet[sym.ID] {
			filtered.HighFanOutSymbols = append(filtered.HighFanOutSymbols, sym)
		}
	}
	for _, sym := range full.HighFanInSymbols {
		if allowedSet[sym.ID] {
			filtered.HighFanInSymbols = append(filtered.HighFanInSymbols, sym)
		}
	}

	linkCounts := make(map[string]int)
	for _, link := range full.Links {
		if allowedSet[link.SymbolID] {
			filtered.Links = append(filtered.Links, link)
			linkCounts[link.RequirementID]++
		}
	}
	for _, req := range full.Requirements {
		if linkCounts[req.ID] > 0 {
			req.LinkedCount = linkCounts[req.ID]
			filtered.Requirements = append(filtered.Requirements, req)
		}
	}
	if len(full.Requirements) > 0 {
		filtered.CoverageRatio = float64(len(filtered.Requirements)) / float64(len(full.Requirements))
	}

	filtered.ScopeContext = a.buildScopeContext(repoID, scope, allowedSymbols, fileByPath)

	return &filtered, nil
}

// AssembleRequirement builds a requirement-scoped snapshot. The flow is
// inverted relative to other scopes: requirement → linked symbols → files.
func (a *Assembler) AssembleRequirement(repoID string, repoSourcePath string, scope ArtifactScope) (*KnowledgeSnapshot, error) {
	full, err := a.Assemble(repoID, repoSourcePath)
	if err != nil {
		return nil, err
	}

	requirementID := scope.ScopePath

	// Fetch the requirement for context.
	req := a.store.GetRequirement(requirementID)
	if req == nil {
		return nil, fmt.Errorf("requirement %s not found", requirementID)
	}

	// Get all non-rejected links for this requirement.
	links := a.store.GetLinksForRequirement(requirementID, false)

	// Budget cap: limit to 200 highest-confidence linked symbols.
	const maxLinkedSymbols = 200
	if len(links) > maxLinkedSymbols {
		sort.Slice(links, func(i, j int) bool {
			return links[i].Confidence > links[j].Confidence
		})
		links = links[:maxLinkedSymbols]
	}

	// Build the directly linked symbol set (no caller/callee expansion).
	linkedSymbolIDs := make(map[string]bool, len(links))
	for _, link := range links {
		linkedSymbolIDs[link.SymbolID] = true
	}

	// Batch fetch linked symbols.
	ids := make([]string, 0, len(linkedSymbolIDs))
	for id := range linkedSymbolIDs {
		ids = append(ids, id)
	}
	symMap := a.store.GetSymbolsByIDs(ids)

	// Build allowed files from linked symbols.
	allowedFiles := make(map[string]bool)
	var linkedSymbols []*graph.StoredSymbol
	for _, sym := range symMap {
		if sym == nil {
			continue
		}
		allowedFiles[sym.FilePath] = true
		linkedSymbols = append(linkedSymbols, sym)
	}

	// Filter full snapshot.
	filtered := *full
	filtered.Modules = nil
	filtered.TestCount = 0
	filtered.EntryPoints = nil
	filtered.PublicAPI = nil
	filtered.TestSymbols = nil
	filtered.ComplexSymbols = nil
	filtered.HighFanOutSymbols = nil
	filtered.HighFanInSymbols = nil
	filtered.Docs = nil // strip docs for requirement scope
	filtered.Requirements = nil
	filtered.Links = nil

	filtered.FileCount = len(allowedFiles)
	filtered.SymbolCount = len(linkedSymbols)

	// Keep only the target requirement.
	filtered.Requirements = []RequirementRef{{
		ID:          req.ID,
		ExternalID:  req.ExternalID,
		Title:       req.Title,
		Priority:    req.Priority,
		LinkedCount: len(links),
	}}

	// Keep only links for this requirement.
	for _, link := range full.Links {
		if link.RequirementID == requirementID && linkedSymbolIDs[link.SymbolID] {
			filtered.Links = append(filtered.Links, link)
		}
	}

	// Populate symbols by role.
	allowedSet := make(map[string]bool, len(linkedSymbols))
	for _, sym := range linkedSymbols {
		allowedSet[sym.ID] = true
		ref := SymbolRef{
			ID: sym.ID, Name: sym.Name, QualifiedName: sym.QualifiedName,
			Kind: sym.Kind, FilePath: sym.FilePath,
			StartLine: sym.StartLine, EndLine: sym.EndLine,
			DocComment: sym.DocComment, LineCount: sym.EndLine - sym.StartLine + 1,
		}
		filtered.ComplexSymbols = append(filtered.ComplexSymbols, ref)
		if isPublicAPI(sym) {
			filtered.PublicAPI = append(filtered.PublicAPI, ref)
		}
	}

	// Keep matching high-fan symbols.
	for _, sym := range full.HighFanOutSymbols {
		if allowedSet[sym.ID] {
			filtered.HighFanOutSymbols = append(filtered.HighFanOutSymbols, sym)
		}
	}
	for _, sym := range full.HighFanInSymbols {
		if allowedSet[sym.ID] {
			filtered.HighFanInSymbols = append(filtered.HighFanInSymbols, sym)
		}
	}

	// Build linked file refs for scope context.
	fileByPath := make(map[string]*graph.File)
	for _, file := range a.store.GetFiles(repoID) {
		fileByPath[file.Path] = file
	}
	var linkedFileRefs []FileRef
	for filePath := range allowedFiles {
		if ref := fileRefFromStored(fileByPath[filePath]); ref != nil {
			linkedFileRefs = append(linkedFileRefs, *ref)
		}
	}

	// Build scope context with full requirement description.
	filtered.ScopeContext = &ScopeContext{
		ScopeType: "requirement",
		ScopePath: requirementID,
		FocusSummary: fmt.Sprintf(
			"Requirement implementation guide for %s: %s",
			req.ExternalID, req.Title,
		),
		TargetRequirement: &RequirementContext{
			ID:          req.ID,
			ExternalID:  req.ExternalID,
			Title:       req.Title,
			Description: req.Description,
			Priority:    req.Priority,
			Tags:        req.Tags,
		},
		KeySymbols:  limitSymbolRefs(symbolRefsFromStored(linkedSymbols), 20),
		LinkedFiles: linkedFileRefs,
	}

	return &filtered, nil
}

func (a *Assembler) allowedFilesForScope(repoID string, scope ArtifactScope) map[string]bool {
	files := a.store.GetFiles(repoID)
	allowed := make(map[string]bool)
	for _, f := range files {
		switch scope.ScopeType {
		case ScopeModule:
			if f.Path == scope.ScopePath || strings.HasPrefix(f.Path, scope.ScopePath+"/") {
				allowed[f.Path] = true
			}
		case ScopeFile:
			if f.Path == scope.ScopePath {
				allowed[f.Path] = true
			}
		case ScopeSymbol:
			if f.Path == scope.FilePath {
				allowed[f.Path] = true
			}
		}
	}
	return allowed
}

func (a *Assembler) allowedSymbolsForScope(repoID string, scope ArtifactScope, allowedFiles map[string]bool) []*graph.StoredSymbol {
	if scope.ScopeType == ScopeSymbol {
		target := a.findScopeSymbol(repoID, scope)
		if target == nil {
			return nil
		}
		syms := a.store.GetSymbolsByFile(repoID, scope.FilePath)
		var results []*graph.StoredSymbol
		for _, sym := range syms {
			if !sym.IsTest {
				results = append(results, sym)
			}
		}
		results = append(results, target)
		for _, callerID := range a.store.GetCallers(target.ID) {
			if caller := a.store.GetSymbol(callerID); caller != nil {
				results = append(results, caller)
			}
		}
		for _, calleeID := range a.store.GetCallees(target.ID) {
			if callee := a.store.GetSymbol(calleeID); callee != nil {
				results = append(results, callee)
			}
		}
		return dedupeStoredSymbols(results)
	}

	all, _ := a.store.GetSymbols(repoID, nil, nil, 0, 0)
	var results []*graph.StoredSymbol
	for _, sym := range all {
		if allowedFiles[sym.FilePath] {
			results = append(results, sym)
		}
	}
	return results
}

func (a *Assembler) buildScopeContext(repoID string, scope ArtifactScope, allowedSymbols []*graph.StoredSymbol, files map[string]*graph.File) *ScopeContext {
	scope = scope.Normalize()
	ctx := &ScopeContext{
		ScopeType: string(scope.ScopeType),
		ScopePath: scope.ScopePath,
	}

	switch scope.ScopeType {
	case ScopeModule:
		ctx.FocusSummary = fmt.Sprintf("Module guide for %s with the files and symbols that shape this area.", scope.ScopePath)
	case ScopeFile:
		ctx.FocusSummary = fmt.Sprintf("File guide for %s with the main symbols, responsibilities, and editing surface.", scope.ScopePath)
		ctx.TargetFile = fileRefFromStored(files[scope.FilePath])
		ctx.KeySymbols = limitSymbolRefs(symbolRefsFromStored(allowedSymbols), 8)
	case ScopeSymbol:
		ctx.FocusSummary = fmt.Sprintf("Symbol guide for %s with nearby file context, callers, callees, and change-impact hints.", scope.ScopePath)
		ctx.TargetFile = fileRefFromStored(files[scope.FilePath])
		target := a.findScopeSymbol(repoID, scope)
		if target != nil {
			ref := symbolRefFromStored(target)
			ctx.TargetSymbol = &ref
			ctx.Callers = limitSymbolRefs(a.symbolRefsByIDs(a.store.GetCallers(target.ID)), 6)
			ctx.Callees = limitSymbolRefs(a.symbolRefsByIDs(a.store.GetCallees(target.ID)), 6)
			ctx.SiblingSymbols = limitSymbolRefs(a.siblingSymbolRefs(repoID, target), 6)
			ctx.KeySymbols = limitSymbolRefs(append([]SymbolRef{ref}, ctx.SiblingSymbols...), 8)
		} else {
			ctx.KeySymbols = limitSymbolRefs(symbolRefsFromStored(allowedSymbols), 8)
		}
	}

	return ctx
}

func (a *Assembler) findScopeSymbol(repoID string, scope ArtifactScope) *graph.StoredSymbol {
	for _, sym := range a.store.GetSymbolsByFile(repoID, scope.FilePath) {
		if sym.Name == scope.SymbolName || sym.QualifiedName == scope.SymbolName {
			return sym
		}
	}
	return nil
}

func (a *Assembler) symbolRefsByIDs(ids []string) []SymbolRef {
	refs := make([]SymbolRef, 0, len(ids))
	for _, id := range ids {
		if sym := a.store.GetSymbol(id); sym != nil {
			refs = append(refs, symbolRefFromStored(sym))
		}
	}
	return dedupeSymbolRefs(refs)
}

func (a *Assembler) siblingSymbolRefs(repoID string, target *graph.StoredSymbol) []SymbolRef {
	siblings := a.store.GetSymbolsByFile(repoID, target.FilePath)
	refs := make([]SymbolRef, 0, len(siblings))
	for _, sym := range siblings {
		if sym.ID == target.ID || sym.IsTest {
			continue
		}
		refs = append(refs, symbolRefFromStored(sym))
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].StartLine == refs[j].StartLine {
			return refs[i].Name < refs[j].Name
		}
		return refs[i].StartLine < refs[j].StartLine
	})
	return refs
}

func dedupeStoredSymbols(symbols []*graph.StoredSymbol) []*graph.StoredSymbol {
	seen := make(map[string]bool, len(symbols))
	results := make([]*graph.StoredSymbol, 0, len(symbols))
	for _, sym := range symbols {
		if sym == nil || seen[sym.ID] {
			continue
		}
		seen[sym.ID] = true
		results = append(results, sym)
	}
	return results
}

func symbolRefsFromStored(symbols []*graph.StoredSymbol) []SymbolRef {
	refs := make([]SymbolRef, 0, len(symbols))
	for _, sym := range symbols {
		if sym == nil {
			continue
		}
		refs = append(refs, symbolRefFromStored(sym))
	}
	return dedupeSymbolRefs(refs)
}

func symbolRefFromStored(sym *graph.StoredSymbol) SymbolRef {
	return SymbolRef{
		ID:            sym.ID,
		Name:          sym.Name,
		QualifiedName: sym.QualifiedName,
		Kind:          sym.Kind,
		Signature:     sym.Signature,
		FilePath:      sym.FilePath,
		StartLine:     sym.StartLine,
		EndLine:       sym.EndLine,
		DocComment:    sym.DocComment,
		LineCount:     sym.EndLine - sym.StartLine + 1,
	}
}

func fileRefFromStored(file *graph.File) *FileRef {
	if file == nil {
		return nil
	}
	return &FileRef{
		Path:       file.Path,
		Language:   file.Language,
		LineCount:  file.LineCount,
		ModulePath: modulePathForFile(file.Path),
	}
}

func dedupeSymbolRefs(symbols []SymbolRef) []SymbolRef {
	seen := make(map[string]bool, len(symbols))
	results := make([]SymbolRef, 0, len(symbols))
	for _, sym := range symbols {
		if sym.ID == "" || seen[sym.ID] {
			continue
		}
		seen[sym.ID] = true
		results = append(results, sym)
	}
	return results
}

func limitSymbolRefs(symbols []SymbolRef, max int) []SymbolRef {
	if len(symbols) <= max {
		return symbols
	}
	return symbols[:max]
}

func buildSourceRevision(repo *graph.Repository) SourceRevision {
	rev := SourceRevision{
		CommitSHA: repo.CommitSHA,
		Branch:    repo.Branch,
	}
	// ContentFingerprint and DocsFingerprint are computed later if needed.
	return rev
}

func (a *Assembler) assembleStructureFromSymbols(snap *KnowledgeSnapshot, repoID string, allSymbols []*graph.StoredSymbol) {
	files := a.store.GetFiles(repoID)
	snap.FileCount = len(files)

	langFiles := map[string]int{}
	for _, f := range files {
		lang := f.Language
		if lang == "" {
			lang = "unknown"
		}
		langFiles[lang]++
	}

	modules := a.store.GetModules(repoID)
	for _, m := range modules {
		snap.Modules = append(snap.Modules, ModuleSummary{
			ID:        m.ID,
			Name:      m.Name,
			Path:      m.Path,
			FileCount: m.FileCount,
		})
	}

	snap.SymbolCount = len(allSymbols)

	langSyms := map[string]int{}
	for _, s := range allSymbols {
		if s.IsTest {
			snap.TestCount++
		}
		lang := s.Language
		if lang == "" {
			lang = "unknown"
		}
		langSyms[lang]++
	}

	for lang, fc := range langFiles {
		snap.Languages = append(snap.Languages, LanguageSummary{
			Language:    lang,
			FileCount:   fc,
			SymbolCount: langSyms[lang],
		})
	}
	sort.Slice(snap.Languages, func(i, j int) bool {
		return snap.Languages[i].FileCount > snap.Languages[j].FileCount
	})
}

func (a *Assembler) assembleSymbolsFromSlice(snap *KnowledgeSnapshot, allSymbols []*graph.StoredSymbol) {
	for _, s := range allSymbols {
		ref := SymbolRef{
			ID:            s.ID,
			Name:          s.Name,
			QualifiedName: s.QualifiedName,
			Kind:          s.Kind,
			FilePath:      s.FilePath,
			StartLine:     s.StartLine,
			EndLine:       s.EndLine,
			DocComment:    s.DocComment,
			LineCount:     s.EndLine - s.StartLine + 1,
		}

		if s.IsTest {
			snap.TestSymbols = append(snap.TestSymbols, ref)
			continue
		}

		// Heuristic: entry points are "main" functions or exported symbols in main packages
		if isEntryPoint(s) {
			snap.EntryPoints = append(snap.EntryPoints, ref)
		}

		// Heuristic: public API symbols (exported, have doc comments)
		if isPublicAPI(s) {
			snap.PublicAPI = append(snap.PublicAPI, ref)
		}

		// Heuristic: complex symbols (large line count)
		if ref.LineCount > 50 {
			snap.ComplexSymbols = append(snap.ComplexSymbols, ref)
		}
	}

	// Sort complex symbols by line count descending.
	sort.Slice(snap.ComplexSymbols, func(i, j int) bool {
		return snap.ComplexSymbols[i].LineCount > snap.ComplexSymbols[j].LineCount
	})

	// Cap to top 20 complex symbols.
	if len(snap.ComplexSymbols) > 20 {
		snap.ComplexSymbols = snap.ComplexSymbols[:20]
	}
}

// assembleCallGraphBatch uses a single batch query instead of per-symbol
// queries. With 16K+ symbols the old approach issued ~33K SurrealDB queries;
// the batch approach issues one.
func (a *Assembler) assembleCallGraphBatch(snap *KnowledgeSnapshot, repoID string, allSymbols []*graph.StoredSymbol) {
	edges := a.store.GetCallEdges(repoID)

	// Compute fan-in / fan-out from edges.
	fanOut := map[string]int{}
	fanIn := map[string]int{}
	for _, e := range edges {
		fanOut[e.CallerID]++
		fanIn[e.CalleeID]++
	}

	// Index symbols by ID for lookup.
	symbolByID := make(map[string]*graph.StoredSymbol, len(allSymbols))
	for _, s := range allSymbols {
		symbolByID[s.ID] = s
	}

	// Collect high fan-out symbols.
	for id, count := range fanOut {
		if count < 5 {
			continue
		}
		s := symbolByID[id]
		if s == nil || s.IsTest {
			continue
		}
		snap.HighFanOutSymbols = append(snap.HighFanOutSymbols, SymbolRef{
			ID:            s.ID,
			Name:          s.Name,
			QualifiedName: s.QualifiedName,
			Kind:          s.Kind,
			FilePath:      s.FilePath,
			StartLine:     s.StartLine,
			EndLine:       s.EndLine,
			FanOut:        count,
			FanIn:         fanIn[s.ID],
		})
	}

	// Collect high fan-in symbols.
	for id, count := range fanIn {
		if count < 5 {
			continue
		}
		s := symbolByID[id]
		if s == nil || s.IsTest {
			continue
		}
		snap.HighFanInSymbols = append(snap.HighFanInSymbols, SymbolRef{
			ID:            s.ID,
			Name:          s.Name,
			QualifiedName: s.QualifiedName,
			Kind:          s.Kind,
			FilePath:      s.FilePath,
			StartLine:     s.StartLine,
			EndLine:       s.EndLine,
			FanOut:        fanOut[s.ID],
			FanIn:         count,
		})
	}

	sort.Slice(snap.HighFanOutSymbols, func(i, j int) bool {
		return snap.HighFanOutSymbols[i].FanOut > snap.HighFanOutSymbols[j].FanOut
	})
	sort.Slice(snap.HighFanInSymbols, func(i, j int) bool {
		return snap.HighFanInSymbols[i].FanIn > snap.HighFanInSymbols[j].FanIn
	})

	if len(snap.HighFanOutSymbols) > 10 {
		snap.HighFanOutSymbols = snap.HighFanOutSymbols[:10]
	}
	if len(snap.HighFanInSymbols) > 10 {
		snap.HighFanInSymbols = snap.HighFanInSymbols[:10]
	}
}

func (a *Assembler) assembleRequirements(snap *KnowledgeSnapshot, repoID string) {
	reqs, _ := a.store.GetRequirements(repoID, 0, 0)
	links := a.store.GetLinksForRepo(repoID)

	reqLinkCounts := map[string]int{}
	linkedReqs := map[string]bool{}
	for _, l := range links {
		if l.Rejected {
			continue
		}
		reqLinkCounts[l.RequirementID]++
		linkedReqs[l.RequirementID] = true

		snap.Links = append(snap.Links, LinkRef{
			RequirementID: l.RequirementID,
			SymbolID:      l.SymbolID,
			Confidence:    l.Confidence,
			Source:        l.Source,
		})
	}

	for _, r := range reqs {
		snap.Requirements = append(snap.Requirements, RequirementRef{
			ID:          r.ID,
			ExternalID:  r.ExternalID,
			Title:       r.Title,
			Priority:    r.Priority,
			LinkedCount: reqLinkCounts[r.ID],
		})
	}

	if len(reqs) > 0 {
		snap.CoverageRatio = float64(len(linkedReqs)) / float64(len(reqs))
	}
}

func (a *Assembler) assembleDocs(snap *KnowledgeSnapshot, repoSourcePath string) {
	if repoSourcePath == "" {
		return
	}

	// Discover docs: README*, docs/**/*.md, top-level *.md
	patterns := []string{
		"README*",
		"*.md",
	}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(repoSourcePath, pattern))
		for _, m := range matches {
			snap.addDoc(repoSourcePath, m)
		}
	}

	// docs/**/*.md
	docsDir := filepath.Join(repoSourcePath, "docs")
	_ = filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			snap.addDoc(repoSourcePath, path)
		}
		return nil
	})

	// Compute docs fingerprint.
	if len(snap.Docs) > 0 {
		h := sha256.New()
		for _, d := range snap.Docs {
			h.Write([]byte(d.Path))
			h.Write([]byte(d.Content))
		}
		snap.SourceRevision.DocsFingerprint = fmt.Sprintf("%x", h.Sum(nil))[:16]
	}
}

func (snap *KnowledgeSnapshot) addDoc(basePath, fullPath string) {
	rel, err := filepath.Rel(basePath, fullPath)
	if err != nil {
		return
	}
	// Deduplicate.
	for _, d := range snap.Docs {
		if d.Path == rel {
			return
		}
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		snap.Docs = append(snap.Docs, DocRef{Path: rel})
		return
	}
	// Cap doc content at 32KB to avoid blowing up snapshots.
	c := string(content)
	if len(c) > 32*1024 {
		c = c[:32*1024]
	}
	snap.Docs = append(snap.Docs, DocRef{Path: rel, Content: c})
}

// isEntryPoint uses heuristics to identify entry-point symbols.
func isEntryPoint(s *graph.StoredSymbol) bool {
	name := strings.ToLower(s.Name)
	return name == "main" || name == "init" || name == "run" || name == "serve" ||
		name == "execute" || name == "start" || name == "app" ||
		strings.HasPrefix(name, "cmd_")
}

// isPublicAPI uses heuristics to identify public API symbols.
func isPublicAPI(s *graph.StoredSymbol) bool {
	if s.IsTest || s.DocComment == "" {
		return false
	}
	// In Go, exported symbols start with uppercase.
	if s.Language == "go" && len(s.Name) > 0 {
		return s.Name[0] >= 'A' && s.Name[0] <= 'Z'
	}
	// For other languages, symbols with doc comments are considered public API.
	return s.Kind == "function" || s.Kind == "method" || s.Kind == "class" || s.Kind == "interface"
}
