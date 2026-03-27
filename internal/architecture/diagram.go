// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package architecture

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// DiagramStore is the subset of graph.GraphStore needed for diagram generation.
type DiagramStore interface {
	GetSymbols(repoID string, query *string, kind *string, limit, offset int) ([]*graph.StoredSymbol, int)
	GetCallEdges(repoID string) []graph.CallEdge
	GetFiles(repoID string) []*graph.File
	GetLinksForRepo(repoID string) []*graph.StoredLink
}

type DiagramOpts struct {
	RepoID       string
	Level        string // "MODULE" or "FILE"
	ModuleFilter *string
	ModuleDepth  int
	MaxNodes     int
}

type ModuleNode struct {
	Path                 string
	SymbolCount          int
	FileCount            int
	RequirementLinkCount int
	InboundEdgeCount     int
	OutboundEdges        []EdgeInfo
}

type EdgeInfo struct {
	TargetPath string
	CallCount  int
}

type DiagramResult struct {
	MermaidSource string
	Modules       []ModuleNode
	Level         string
	TotalModules  int
	ShownModules  int
	Truncated     bool
}

// BuildDiagram generates an architecture diagram from the symbol graph.
func BuildDiagram(store DiagramStore, opts DiagramOpts) (*DiagramResult, error) {
	if opts.Level == "FILE" && opts.ModuleFilter != nil {
		return buildFileLevelDiagram(store, opts)
	}
	return buildModuleLevelDiagram(store, opts)
}

// ModuleFromPath extracts a directory prefix at the given depth from a file path.
func ModuleFromPath(filePath string, depth int) string {
	dir := filepath.Dir(filePath)
	if dir == "." || dir == "" {
		return "(root)"
	}
	parts := strings.Split(filepath.ToSlash(dir), "/")
	if len(parts) > depth {
		parts = parts[:depth]
	}
	result := strings.Join(parts, "/")
	if result == "" || result == "." {
		return "(root)"
	}
	return result
}

func buildModuleLevelDiagram(store DiagramStore, opts DiagramOpts) (*DiagramResult, error) {
	symbols, _ := store.GetSymbols(opts.RepoID, nil, nil, 0, 0)
	edges := store.GetCallEdges(opts.RepoID)
	files := store.GetFiles(opts.RepoID)
	links := store.GetLinksForRepo(opts.RepoID)

	// Build symbol ID -> file_path lookup
	symPathMap := make(map[string]string, len(symbols))
	for _, s := range symbols {
		symPathMap[s.ID] = s.FilePath
	}

	// Build linked symbol set
	linkedSymbols := make(map[string]bool, len(links))
	for _, l := range links {
		linkedSymbols[l.SymbolID] = true
	}

	// Aggregate symbols per module
	moduleSymbolCount := make(map[string]int)
	moduleLinkCount := make(map[string]int)
	for _, s := range symbols {
		mod := ModuleFromPath(s.FilePath, opts.ModuleDepth)
		moduleSymbolCount[mod]++
		if linkedSymbols[s.ID] {
			moduleLinkCount[mod]++
		}
	}

	// Aggregate files per module
	moduleFileCount := make(map[string]int)
	for _, f := range files {
		mod := ModuleFromPath(f.Path, opts.ModuleDepth)
		moduleFileCount[mod]++
	}

	// Aggregate call edges into module-level edges
	type edgeKey struct{ src, tgt string }
	edgeCounts := make(map[edgeKey]int)
	for _, e := range edges {
		srcPath, ok1 := symPathMap[e.CallerID]
		tgtPath, ok2 := symPathMap[e.CalleeID]
		if !ok1 || !ok2 {
			continue
		}
		srcMod := ModuleFromPath(srcPath, opts.ModuleDepth)
		tgtMod := ModuleFromPath(tgtPath, opts.ModuleDepth)
		if srcMod == tgtMod {
			continue // skip self-edges
		}
		edgeCounts[edgeKey{srcMod, tgtMod}]++
	}

	// Collect all module paths
	allModules := make(map[string]bool)
	for mod := range moduleSymbolCount {
		allModules[mod] = true
	}
	for mod := range moduleFileCount {
		allModules[mod] = true
	}

	totalModules := len(allModules)

	// Build inbound edge counts
	inboundCount := make(map[string]int)
	for ek := range edgeCounts {
		inboundCount[ek.tgt]++
	}

	// Build outbound edges per module
	outboundEdges := make(map[string][]EdgeInfo)
	for ek, count := range edgeCounts {
		outboundEdges[ek.src] = append(outboundEdges[ek.src], EdgeInfo{
			TargetPath: ek.tgt,
			CallCount:  count,
		})
	}

	// Sort module paths
	sortedModules := make([]string, 0, len(allModules))
	for mod := range allModules {
		sortedModules = append(sortedModules, mod)
	}

	// Truncation: keep top N by connectivity
	truncated := false
	if len(sortedModules) > opts.MaxNodes {
		truncated = true
		sort.Slice(sortedModules, func(i, j int) bool {
			ci := inboundCount[sortedModules[i]] + len(outboundEdges[sortedModules[i]])
			cj := inboundCount[sortedModules[j]] + len(outboundEdges[sortedModules[j]])
			if ci != cj {
				return ci > cj
			}
			return sortedModules[i] < sortedModules[j]
		})

		// Collapse overflow into (other)
		kept := make(map[string]bool)
		for _, m := range sortedModules[:opts.MaxNodes-1] {
			kept[m] = true
		}
		otherSymbols := 0
		otherFiles := 0
		otherLinks := 0
		otherInbound := 0
		var otherOutbound []EdgeInfo
		collapsedCount := 0
		for _, m := range sortedModules[opts.MaxNodes-1:] {
			otherSymbols += moduleSymbolCount[m]
			otherFiles += moduleFileCount[m]
			otherLinks += moduleLinkCount[m]
			otherInbound += inboundCount[m]
			collapsedCount++
		}

		// Merge edges involving collapsed modules
		otherEdgeCounts := make(map[string]int)
		for ek, count := range edgeCounts {
			srcKept := kept[ek.src]
			tgtKept := kept[ek.tgt]
			if !srcKept && !tgtKept {
				continue // both collapsed, skip
			}
			if !srcKept {
				otherEdgeCounts[ek.tgt] += count
			}
			if !tgtKept {
				// edge from kept to collapsed
				found := false
				for i, oe := range outboundEdges[ek.src] {
					if oe.TargetPath == ek.tgt {
						outboundEdges[ek.src][i].TargetPath = "(other)"
						found = true
						break
					}
				}
				if !found {
					outboundEdges[ek.src] = append(outboundEdges[ek.src], EdgeInfo{
						TargetPath: "(other)",
						CallCount:  count,
					})
				}
			}
		}
		for tgt, count := range otherEdgeCounts {
			otherOutbound = append(otherOutbound, EdgeInfo{TargetPath: tgt, CallCount: count})
		}

		otherName := "(other)"
		moduleSymbolCount[otherName] = otherSymbols
		moduleFileCount[otherName] = otherFiles
		moduleLinkCount[otherName] = otherLinks
		inboundCount[otherName] = otherInbound
		outboundEdges[otherName] = otherOutbound

		sortedModules = append(sortedModules[:opts.MaxNodes-1], otherName)
		_ = collapsedCount
	}

	sort.Strings(sortedModules)

	// Build module nodes
	modules := make([]ModuleNode, 0, len(sortedModules))
	for _, mod := range sortedModules {
		oe := outboundEdges[mod]
		sort.Slice(oe, func(i, j int) bool {
			if oe[i].CallCount != oe[j].CallCount {
				return oe[i].CallCount > oe[j].CallCount
			}
			return oe[i].TargetPath < oe[j].TargetPath
		})
		modules = append(modules, ModuleNode{
			Path:                 mod,
			SymbolCount:          moduleSymbolCount[mod],
			FileCount:            moduleFileCount[mod],
			RequirementLinkCount: moduleLinkCount[mod],
			InboundEdgeCount:     inboundCount[mod],
			OutboundEdges:        oe,
		})
	}

	mermaid := GenerateModuleMermaid(modules, truncated, totalModules)

	return &DiagramResult{
		MermaidSource: mermaid,
		Modules:       modules,
		Level:         "MODULE",
		TotalModules:  totalModules,
		ShownModules:  len(modules),
		Truncated:     truncated,
	}, nil
}

func buildFileLevelDiagram(store DiagramStore, opts DiagramOpts) (*DiagramResult, error) {
	prefix := *opts.ModuleFilter
	symbols, _ := store.GetSymbols(opts.RepoID, nil, nil, 0, 0)
	edges := store.GetCallEdges(opts.RepoID)

	// Build symbol ID -> file_path lookup and filter by module
	symPathMap := make(map[string]string, len(symbols))
	moduleSymIDs := make(map[string]bool)
	for _, s := range symbols {
		symPathMap[s.ID] = s.FilePath
		if strings.HasPrefix(filepath.ToSlash(s.FilePath), prefix) {
			moduleSymIDs[s.ID] = true
		}
	}

	// Aggregate file-level edges
	type edgeKey struct{ src, tgt string }
	edgeCounts := make(map[edgeKey]int)
	fileSet := make(map[string]bool)
	externalFileSet := make(map[string]bool)

	for _, e := range edges {
		srcPath, ok1 := symPathMap[e.CallerID]
		tgtPath, ok2 := symPathMap[e.CalleeID]
		if !ok1 || !ok2 {
			continue
		}
		srcInModule := moduleSymIDs[e.CallerID]
		tgtInModule := moduleSymIDs[e.CalleeID]
		if !srcInModule && !tgtInModule {
			continue // neither in this module
		}
		if srcPath == tgtPath {
			continue // same file
		}
		edgeCounts[edgeKey{srcPath, tgtPath}]++
		if srcInModule {
			fileSet[srcPath] = true
		} else {
			externalFileSet[srcPath] = true
		}
		if tgtInModule {
			fileSet[tgtPath] = true
		} else {
			externalFileSet[tgtPath] = true
		}
	}

	// Count symbols per file (within module)
	fileSymbolCount := make(map[string]int)
	for _, s := range symbols {
		if strings.HasPrefix(filepath.ToSlash(s.FilePath), prefix) {
			fileSymbolCount[s.FilePath]++
			fileSet[s.FilePath] = true
		}
	}

	// Build sorted file lists
	internalFiles := make([]string, 0, len(fileSet))
	for f := range fileSet {
		internalFiles = append(internalFiles, f)
	}
	sort.Strings(internalFiles)

	externalFiles := make([]string, 0, len(externalFileSet))
	for f := range externalFileSet {
		if !fileSet[f] {
			externalFiles = append(externalFiles, f)
		}
	}
	sort.Strings(externalFiles)

	// Truncate if too many nodes
	totalFiles := len(internalFiles) + len(externalFiles)
	truncated := false
	maxFiles := opts.MaxNodes
	if maxFiles <= 0 {
		maxFiles = 50
	}
	if totalFiles > maxFiles {
		truncated = true
		if len(internalFiles) > maxFiles-5 {
			internalFiles = internalFiles[:maxFiles-5]
		}
		remaining := maxFiles - len(internalFiles)
		if len(externalFiles) > remaining {
			externalFiles = externalFiles[:remaining]
		}
	}

	// Build inbound counts
	inboundCount := make(map[string]int)
	outboundEdges := make(map[string][]EdgeInfo)
	allowedFiles := make(map[string]bool)
	for _, f := range internalFiles {
		allowedFiles[f] = true
	}
	for _, f := range externalFiles {
		allowedFiles[f] = true
	}

	for ek, count := range edgeCounts {
		if !allowedFiles[ek.src] || !allowedFiles[ek.tgt] {
			continue
		}
		inboundCount[ek.tgt]++
		outboundEdges[ek.src] = append(outboundEdges[ek.src], EdgeInfo{
			TargetPath: ek.tgt,
			CallCount:  count,
		})
	}

	// Build module nodes (one per file)
	modules := make([]ModuleNode, 0, len(internalFiles)+len(externalFiles))
	for _, f := range internalFiles {
		oe := outboundEdges[f]
		sort.Slice(oe, func(i, j int) bool { return oe[i].CallCount > oe[j].CallCount })
		modules = append(modules, ModuleNode{
			Path:             f,
			SymbolCount:      fileSymbolCount[f],
			InboundEdgeCount: inboundCount[f],
			OutboundEdges:    oe,
		})
	}
	for _, f := range externalFiles {
		oe := outboundEdges[f]
		sort.Slice(oe, func(i, j int) bool { return oe[i].CallCount > oe[j].CallCount })
		modules = append(modules, ModuleNode{
			Path:             f,
			InboundEdgeCount: inboundCount[f],
			OutboundEdges:    oe,
		})
	}

	mermaid := GenerateFileMermaid(prefix, internalFiles, externalFiles, fileSymbolCount, outboundEdges)

	return &DiagramResult{
		MermaidSource: mermaid,
		Modules:       modules,
		Level:         "FILE",
		TotalModules:  totalFiles,
		ShownModules:  len(modules),
		Truncated:     truncated,
	}, nil
}
