package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	"github.com/sourcebridge/sourcebridge/internal/architecture"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

type repositoryUnderstandingMetadata struct {
	FirstPassSections []map[string]string `json:"first_pass_sections,omitempty"`
	Resume            map[string]any      `json:"resume,omitempty"`
}

type cliffNotesSectionMetadata struct {
	SectionKey          string `json:"section_key,omitempty"`
	RefinementTier      string `json:"refinement_tier,omitempty"`
	RefinedWithEvidence bool   `json:"refined_with_evidence,omitempty"`
	EvidenceRevisionFP  string `json:"evidence_revision_fp,omitempty"`
	RendererVersion     string `json:"renderer_version,omitempty"`
	UnderstandingID     string `json:"understanding_id,omitempty"`
}

type cliffNotesRenderPlan struct {
	RenderOnly            bool
	SelectedSectionTitles []string
	UnderstandingDepth    string
	RelevanceProfile      string
}

type architectureDiagramScaffold struct {
	Level         string                            `json:"level"`
	MermaidSource string                            `json:"mermaid_source"`
	Modules       []architectureDiagramScaffoldNode `json:"modules"`
}

type architectureDiagramScaffoldNode struct {
	Path          string   `json:"path"`
	FilePaths     []string `json:"file_paths,omitempty"`
	OutboundPaths []string `json:"outbound_paths,omitempty"`
}

type architectureDiagramSectionMetadata struct {
	RawMermaidSource     string                        `json:"raw_mermaid_source,omitempty"`
	ValidationStatus     string                        `json:"validation_status,omitempty"`
	RepairSummary        string                        `json:"repair_summary,omitempty"`
	InferredEdges        []string                      `json:"inferred_edges,omitempty"`
	ContradictoryEdges   []string                      `json:"contradictory_edges,omitempty"`
	GraphAlignmentStatus string                        `json:"graph_alignment_status,omitempty"`
	GenerationStrategy   string                        `json:"generation_strategy,omitempty"`
	Components           []architectureSystemComponent `json:"components,omitempty"`
	ExecutionMermaid     string                        `json:"execution_mermaid_source,omitempty"`
	ExecutionSummary     string                        `json:"execution_summary,omitempty"`
	SystemSummary        string                        `json:"system_summary,omitempty"`
	DetailSubsystemName  string                        `json:"detail_subsystem_name,omitempty"`
	DetailCandidates     []string                      `json:"detail_candidate_subsystems,omitempty"`
}

type architectureDiagramPromptBundle struct {
	RepositoryID            string                            `json:"repository_id"`
	RepositoryName          string                            `json:"repository_name"`
	SourceRevision          knowledgepkg.SourceRevision       `json:"source_revision"`
	Languages               []knowledgepkg.LanguageSummary    `json:"languages,omitempty"`
	Modules                 []knowledgepkg.ModuleSummary      `json:"modules,omitempty"`
	EntryPoints             []knowledgepkg.SymbolRef          `json:"entry_points,omitempty"`
	PublicAPI               []knowledgepkg.SymbolRef          `json:"public_api,omitempty"`
	HighFanOutSymbols       []knowledgepkg.SymbolRef          `json:"high_fan_out_symbols,omitempty"`
	HighFanInSymbols        []knowledgepkg.SymbolRef          `json:"high_fan_in_symbols,omitempty"`
	RepresentativeFiles     []knowledgepkg.FileRef            `json:"representative_files,omitempty"`
	DocumentationHighlights []architectureDiagramDocHighlight `json:"documentation_highlights,omitempty"`
	RepositoryUnderstanding []map[string]string               `json:"repository_understanding,omitempty"`
	CliffNotesHighlights    []map[string]string               `json:"cliff_notes_highlights,omitempty"`
	SystemComponents        []architectureSystemComponent     `json:"system_components,omitempty"`
	SystemFlows             []architectureSystemFlow          `json:"system_flows,omitempty"`
	DeterministicScaffold   architectureDiagramScaffold       `json:"deterministic_scaffold"`
}

type architectureDiagramDocHighlight struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

type architectureSystemComponent struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind"`
	ModulePaths []string `json:"module_paths,omitempty"`
}

type architectureSystemFlow struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Summary  string `json:"summary,omitempty"`
}

func buildArchitectureDiagramScaffold(store graphstore.GraphStore, repoID string) ([]byte, error) {
	if store == nil || strings.TrimSpace(repoID) == "" {
		return nil, nil
	}
	result, err := architecture.BuildDiagram(store, architecture.DiagramOpts{
		RepoID:      repoID,
		Level:       "MODULE",
		ModuleDepth: 2,
		MaxNodes:    14,
	})
	if err != nil {
		return nil, err
	}
	fileBuckets := map[string][]string{}
	for _, file := range store.GetFiles(repoID) {
		module := architecture.ModuleFromPath(file.Path, 2)
		fileBuckets[module] = append(fileBuckets[module], file.Path)
	}
	payload := architectureDiagramScaffold{
		Level:         result.Level,
		MermaidSource: result.MermaidSource,
		Modules:       make([]architectureDiagramScaffoldNode, 0, len(result.Modules)),
	}
	for _, mod := range result.Modules {
		files := append([]string(nil), fileBuckets[mod.Path]...)
		sort.Strings(files)
		if len(files) > 4 {
			files = files[:4]
		}
		outbound := make([]string, 0, len(mod.OutboundEdges))
		for _, edge := range mod.OutboundEdges {
			outbound = append(outbound, edge.TargetPath)
		}
		sort.Strings(outbound)
		payload.Modules = append(payload.Modules, architectureDiagramScaffoldNode{
			Path:          mod.Path,
			FilePaths:     files,
			OutboundPaths: outbound,
		})
	}
	return json.Marshal(payload)
}

func architectureDiagramMetadataJSON(resp *knowledgev1.GenerateArchitectureDiagramResponse, bundle *architectureDiagramPromptBundle) string {
	if resp == nil || bundle == nil {
		return ""
	}
	meta := architectureDiagramSectionMetadata{
		RawMermaidSource:     strings.TrimSpace(resp.RawMermaidSource),
		ValidationStatus:     strings.TrimSpace(resp.ValidationStatus),
		RepairSummary:        strings.TrimSpace(resp.RepairSummary),
		InferredEdges:        append([]string(nil), resp.InferredEdges...),
		ContradictoryEdges:   architectureDiagramContradictoryEdges(resp, bundle),
		GraphAlignmentStatus: architectureDiagramGraphAlignmentStatus(resp, bundle),
		GenerationStrategy:   architectureDiagramGenerationStrategy(resp),
		Components:           append([]architectureSystemComponent(nil), bundle.SystemComponents...),
		ExecutionMermaid:     buildArchitectureExecutionViewMermaid(*bundle),
		ExecutionSummary:     buildArchitectureExecutionSummary(*bundle),
		SystemSummary:        strings.TrimSpace(resp.DiagramSummary),
		DetailSubsystemName:  strings.TrimSpace(resp.GetDetailSubsystemName()),
		DetailCandidates:     append([]string(nil), resp.GetDetailCandidateSubsystems()...),
	}
	if meta.RawMermaidSource == "" &&
		meta.ValidationStatus == "" &&
		meta.RepairSummary == "" &&
		len(meta.InferredEdges) == 0 &&
		len(meta.ContradictoryEdges) == 0 &&
		meta.GraphAlignmentStatus == "" &&
		meta.GenerationStrategy == "" &&
		len(meta.Components) == 0 &&
		meta.ExecutionMermaid == "" &&
		meta.ExecutionSummary == "" &&
		meta.SystemSummary == "" {
		return ""
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(raw)
}

func architectureDiagramDetailMetadataJSON(resp *knowledgev1.GenerateArchitectureDiagramResponse) string {
	if resp == nil {
		return ""
	}
	meta := architectureDiagramSectionMetadata{
		RawMermaidSource:    strings.TrimSpace(resp.GetDetailRawMermaidSource()),
		ValidationStatus:    strings.TrimSpace(resp.GetDetailValidationStatus()),
		RepairSummary:       strings.TrimSpace(resp.GetDetailRepairSummary()),
		SystemSummary:       strings.TrimSpace(resp.GetDetailDiagramSummary()),
		DetailSubsystemName: strings.TrimSpace(resp.GetDetailSubsystemName()),
		DetailCandidates:    append([]string(nil), resp.GetDetailCandidateSubsystems()...),
	}
	if meta.RawMermaidSource == "" &&
		meta.ValidationStatus == "" &&
		meta.RepairSummary == "" &&
		meta.SystemSummary == "" &&
		meta.DetailSubsystemName == "" &&
		len(meta.DetailCandidates) == 0 {
		return ""
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(raw)
}

func architectureDiagramContradictoryEdges(resp *knowledgev1.GenerateArchitectureDiagramResponse, bundle *architectureDiagramPromptBundle) []string {
	if resp == nil || bundle == nil {
		return nil
	}
	reverse := architectureDiagramReverseEdgeSet(bundle.SystemFlows)
	if len(reverse) == 0 {
		return nil
	}
	var out []string
	for _, edge := range resp.InferredEdges {
		if _, ok := reverse[edge]; ok {
			out = append(out, edge)
		}
	}
	sort.Strings(out)
	return out
}

func architectureDiagramGraphAlignmentStatus(resp *knowledgev1.GenerateArchitectureDiagramResponse, bundle *architectureDiagramPromptBundle) string {
	if resp == nil || bundle == nil {
		return ""
	}
	if len(architectureDiagramContradictoryEdges(resp, bundle)) > 0 {
		return "contradictory"
	}
	if len(resp.InferredEdges) > 0 {
		return "inferred"
	}
	if len(bundle.SystemFlows) > 0 {
		return "aligned"
	}
	return ""
}

func architectureDiagramReverseEdgeSet(flows []architectureSystemFlow) map[string]struct{} {
	if len(flows) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(flows))
	for _, flow := range flows {
		if flow.SourceID == "" || flow.TargetID == "" {
			continue
		}
		out[fmt.Sprintf("%s -> %s", flow.TargetID, flow.SourceID)] = struct{}{}
	}
	return out
}

func architectureDiagramGenerationStrategy(resp *knowledgev1.GenerateArchitectureDiagramResponse) string {
	if resp == nil {
		return ""
	}
	repairSummary := strings.ToLower(strings.TrimSpace(resp.GetRepairSummary()))
	switch {
	case strings.Contains(repairSummary, "fell back to deterministic system view"):
		return "fallback"
	case strings.TrimSpace(resp.GetValidationStatus()) == "repaired":
		return "repaired"
	default:
		return "llm"
	}
}

func buildArchitectureDiagramPromptBundle(
	store knowledgepkg.KnowledgeStore,
	repoID string,
	audience knowledgepkg.Audience,
	snap *knowledgepkg.KnowledgeSnapshot,
	understanding *knowledgepkg.RepositoryUnderstanding,
	scaffoldJSON []byte,
) ([]byte, error) {
	if snap == nil {
		return nil, fmt.Errorf("snapshot is required")
	}
	bundle := architectureDiagramPromptBundle{
		RepositoryID:      snap.RepositoryID,
		RepositoryName:    snap.RepositoryName,
		SourceRevision:    snap.SourceRevision,
		Languages:         append([]knowledgepkg.LanguageSummary(nil), snap.Languages...),
		Modules:           append([]knowledgepkg.ModuleSummary(nil), snap.Modules...),
		EntryPoints:       architectureDiagramCapSymbols(snap.EntryPoints, 8),
		PublicAPI:         architectureDiagramCapSymbols(snap.PublicAPI, 8),
		HighFanOutSymbols: architectureDiagramCapSymbols(snap.HighFanOutSymbols, 6),
		HighFanInSymbols:  architectureDiagramCapSymbols(snap.HighFanInSymbols, 6),
	}
	if len(bundle.Languages) > 8 {
		bundle.Languages = bundle.Languages[:8]
	}
	if len(bundle.Modules) > 14 {
		bundle.Modules = bundle.Modules[:14]
	}
	bundle.RepresentativeFiles = architectureDiagramRepresentativeFiles(snap, scaffoldJSON)
	bundle.DocumentationHighlights = architectureDiagramDocHighlights(snap.Docs, 3)
	bundle.RepositoryUnderstanding = architectureDiagramUnderstandingHighlights(understanding)
	bundle.CliffNotesHighlights = architectureDiagramCliffNotesHighlights(store, repoID, audience)
	if len(scaffoldJSON) > 0 {
		if err := json.Unmarshal(scaffoldJSON, &bundle.DeterministicScaffold); err != nil {
			return nil, fmt.Errorf("unmarshal architecture scaffold: %w", err)
		}
	}
	bundle.SystemComponents, bundle.SystemFlows = architectureDiagramSystemView(bundle.DeterministicScaffold)
	return json.Marshal(bundle)
}

func architectureDiagramSystemView(
	scaffold architectureDiagramScaffold,
) ([]architectureSystemComponent, []architectureSystemFlow) {
	if len(scaffold.Modules) == 0 {
		return nil, nil
	}
	componentOrder := []architectureSystemComponent{
		{ID: "user_interfaces", Label: "User Interfaces", Kind: "interface"},
		{ID: "api_auth", Label: "API & Auth", Kind: "service"},
		{ID: "knowledge_orchestration", Label: "Knowledge Orchestration", Kind: "orchestration"},
		{ID: "background_workers", Label: "Background Workers", Kind: "worker"},
		{ID: "code_graph_index", Label: "Code Graph & Index", Kind: "analysis"},
		{ID: "repository_access", Label: "Repository Access", Kind: "integration"},
		{ID: "persistence", Label: "Persistence", Kind: "storage"},
		{ID: "llm_provider", Label: "LLM Provider", Kind: "external"},
	}
	componentByID := make(map[string]*architectureSystemComponent, len(componentOrder))
	for i := range componentOrder {
		componentByID[componentOrder[i].ID] = &componentOrder[i]
	}
	for _, mod := range scaffold.Modules {
		componentID := architectureComponentForModule(mod.Path)
		if componentID == "" {
			continue
		}
		component := componentByID[componentID]
		if component == nil {
			continue
		}
		component.ModulePaths = append(component.ModulePaths, mod.Path)
	}
	if componentByID["background_workers"] != nil && len(componentByID["background_workers"].ModulePaths) > 0 {
		componentByID["llm_provider"].ModulePaths = []string{"external:llm_provider"}
	}
	components := make([]architectureSystemComponent, 0, len(componentOrder))
	for _, component := range componentOrder {
		if len(component.ModulePaths) == 0 {
			continue
		}
		sort.Strings(component.ModulePaths)
		components = append(components, component)
	}
	present := make(map[string]bool, len(components))
	for _, component := range components {
		present[component.ID] = true
	}
	candidates := []architectureSystemFlow{
		{SourceID: "user_interfaces", TargetID: "api_auth", Summary: "HTTP/API requests"},
		{SourceID: "api_auth", TargetID: "knowledge_orchestration", Summary: "routes generation requests"},
		{SourceID: "knowledge_orchestration", TargetID: "background_workers", Summary: "dispatches jobs"},
		{SourceID: "knowledge_orchestration", TargetID: "code_graph_index", Summary: "loads repository understanding"},
		{SourceID: "background_workers", TargetID: "repository_access", Summary: "reads repository snapshots"},
		{SourceID: "background_workers", TargetID: "code_graph_index", Summary: "reads graph and summaries"},
		{SourceID: "background_workers", TargetID: "persistence", Summary: "stores artifacts and job state"},
		{SourceID: "background_workers", TargetID: "llm_provider", Summary: "calls LLM provider"},
		{SourceID: "api_auth", TargetID: "persistence", Summary: "reads and writes metadata"},
	}
	flows := make([]architectureSystemFlow, 0, len(candidates))
	for _, flow := range candidates {
		if !present[flow.SourceID] || !present[flow.TargetID] {
			continue
		}
		flows = append(flows, flow)
	}
	return components, flows
}

func buildArchitectureExecutionViewMermaid(bundle architectureDiagramPromptBundle) string {
	if len(bundle.SystemComponents) == 0 {
		return ""
	}
	componentsByID := make(map[string]architectureSystemComponent, len(bundle.SystemComponents))
	for _, component := range bundle.SystemComponents {
		componentsByID[component.ID] = component
	}
	required := []string{
		"user_interfaces",
		"api_auth",
		"knowledge_orchestration",
		"background_workers",
	}
	for _, id := range required {
		if _, ok := componentsByID[id]; !ok {
			return ""
		}
	}
	lines := []string{
		"flowchart TD",
		`    user["User"]`,
		`    subgraph request_path["Primary Request Path"]`,
		`        user_interfaces["User Interfaces"]`,
		`        api_auth["API & Auth"]`,
		`        knowledge_orchestration["Knowledge Orchestration"]`,
		`        background_workers["Background Workers"]`,
		"    end",
		`    subgraph supporting_systems["Supporting Systems"]`,
	}
	for _, id := range []string{"repository_access", "code_graph_index", "persistence", "llm_provider"} {
		component, ok := componentsByID[id]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf(`        %s["%s"]`, id, component.Label))
	}
	lines = append(lines,
		"    end",
		"    classDef primary fill:#1f3b5b,stroke:#9fd3ff,color:#f5fbff,stroke-width:2px;",
		"    classDef support fill:#263238,stroke:#90a4ae,color:#f5f7fa,stroke-width:1px;",
		"    classDef external fill:#3f2f21,stroke:#f2c078,color:#fff7ea,stroke-width:1px;",
		"    class user,user_interfaces,api_auth,knowledge_orchestration,background_workers primary;",
		"    class code_graph_index,repository_access,persistence support;",
		"    class llm_provider external;",
		"    user --> user_interfaces",
		`    user_interfaces -->|HTTP/API requests| api_auth`,
		`    api_auth -->|routes generation requests| knowledge_orchestration`,
		`    knowledge_orchestration -->|dispatches jobs| background_workers`,
	)
	if _, ok := componentsByID["repository_access"]; ok {
		lines = append(lines, `    background_workers -->|reads repository snapshots| repository_access`)
	}
	if _, ok := componentsByID["code_graph_index"]; ok {
		lines = append(lines, `    background_workers -->|loads graph and summaries| code_graph_index`)
	}
	if _, ok := componentsByID["persistence"]; ok {
		lines = append(lines,
			`    background_workers -->|stores artifacts and job state| persistence`,
			`    api_auth -->|reads and writes metadata| persistence`,
		)
	}
	if _, ok := componentsByID["llm_provider"]; ok {
		lines = append(lines, `    background_workers -->|calls LLM provider| llm_provider`)
	}
	return strings.Join(lines, "\n")
}

func buildArchitectureExecutionSummary(bundle architectureDiagramPromptBundle) string {
	componentsByID := make(map[string]architectureSystemComponent, len(bundle.SystemComponents))
	for _, component := range bundle.SystemComponents {
		componentsByID[component.ID] = component
	}
	if len(componentsByID) == 0 {
		return ""
	}
	parts := []string{
		"Requests move from the user interfaces into the API",
		"the API hands knowledge work to the orchestration layer",
		"background workers execute generation jobs",
	}
	if _, ok := componentsByID["code_graph_index"]; ok {
		parts = append(parts, "workers ground that work in the code graph and repository understanding")
	}
	if _, ok := componentsByID["persistence"]; ok {
		parts = append(parts, "results and job state are persisted")
	}
	if _, ok := componentsByID["llm_provider"]; ok {
		parts = append(parts, "and the worker calls the configured LLM provider when synthesis is needed")
	}
	return strings.Join(parts, ", ") + "."
}

func architectureComponentForModule(modulePath string) string {
	modulePath = strings.Trim(strings.TrimSpace(modulePath), "/")
	switch {
	case modulePath == "", modulePath == ".", modulePath == "root":
		return "supporting"
	case modulePath == "cmd", strings.HasPrefix(modulePath, "cmd/"), modulePath == "cli", strings.HasPrefix(modulePath, "cli/"), modulePath == "web", strings.HasPrefix(modulePath, "web/"), modulePath == "plugins", strings.HasPrefix(modulePath, "plugins/"):
		return "user_interfaces"
	case modulePath == "internal/api", strings.HasPrefix(modulePath, "internal/api/"), modulePath == "internal/auth", strings.HasPrefix(modulePath, "internal/auth/"):
		return "api_auth"
	case modulePath == "internal/knowledge", strings.HasPrefix(modulePath, "internal/knowledge/"):
		return "knowledge_orchestration"
	case modulePath == "internal/worker", strings.HasPrefix(modulePath, "internal/worker/"), modulePath == "workers", strings.HasPrefix(modulePath, "workers/"):
		return "background_workers"
	case modulePath == "internal/graph", strings.HasPrefix(modulePath, "internal/graph/"), modulePath == "internal/indexer", strings.HasPrefix(modulePath, "internal/indexer/"):
		return "code_graph_index"
	case modulePath == "internal/git", strings.HasPrefix(modulePath, "internal/git/"), modulePath == "internal/source", strings.HasPrefix(modulePath, "internal/source/"):
		return "repository_access"
	case modulePath == "internal/db", strings.HasPrefix(modulePath, "internal/db/"):
		return "persistence"
	default:
		return ""
	}
}

func architectureDiagramCapSymbols(in []knowledgepkg.SymbolRef, limit int) []knowledgepkg.SymbolRef {
	if limit <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) < limit {
		limit = len(in)
	}
	out := make([]knowledgepkg.SymbolRef, 0, limit)
	for _, sym := range in[:limit] {
		sym.DocComment = truncateForArchitectureBundle(sym.DocComment, 220)
		out = append(out, sym)
	}
	return out
}

func architectureDiagramRepresentativeFiles(
	snap *knowledgepkg.KnowledgeSnapshot,
	scaffoldJSON []byte,
) []knowledgepkg.FileRef {
	if snap == nil {
		return nil
	}
	seen := map[string]struct{}{}
	files := make([]knowledgepkg.FileRef, 0, 12)
	add := func(filePath string) {
		filePath = strings.TrimSpace(filePath)
		if filePath == "" {
			return
		}
		if _, ok := seen[filePath]; ok {
			return
		}
		seen[filePath] = struct{}{}
		files = append(files, knowledgepkg.FileRef{
			Path:       filePath,
			ModulePath: path.Dir(filePath),
		})
	}
	if len(scaffoldJSON) > 0 {
		var scaffold architectureDiagramScaffold
		if err := json.Unmarshal(scaffoldJSON, &scaffold); err == nil {
			for _, mod := range scaffold.Modules {
				for _, filePath := range mod.FilePaths {
					add(filePath)
					if len(files) >= 12 {
						return files
					}
				}
			}
		}
	}
	for _, sym := range snap.EntryPoints {
		add(sym.FilePath)
		if len(files) >= 12 {
			return files
		}
	}
	for _, sym := range snap.PublicAPI {
		add(sym.FilePath)
		if len(files) >= 12 {
			return files
		}
	}
	for _, sym := range snap.HighFanOutSymbols {
		add(sym.FilePath)
		if len(files) >= 12 {
			return files
		}
	}
	return files
}

func architectureDiagramDocHighlights(
	docs []knowledgepkg.DocRef,
	limit int,
) []architectureDiagramDocHighlight {
	if limit <= 0 || len(docs) == 0 {
		return nil
	}
	if len(docs) < limit {
		limit = len(docs)
	}
	out := make([]architectureDiagramDocHighlight, 0, limit)
	for _, doc := range docs[:limit] {
		out = append(out, architectureDiagramDocHighlight{
			Path:    doc.Path,
			Content: truncateForArchitectureBundle(doc.Content, 600),
		})
	}
	return out
}

func architectureDiagramUnderstandingHighlights(understanding *knowledgepkg.RepositoryUnderstanding) []map[string]string {
	if understanding == nil || strings.TrimSpace(understanding.Metadata) == "" {
		return nil
	}
	var meta repositoryUnderstandingMetadata
	if err := json.Unmarshal([]byte(understanding.Metadata), &meta); err != nil {
		return nil
	}
	out := make([]map[string]string, 0, min(len(meta.FirstPassSections), 8))
	for _, sec := range meta.FirstPassSections {
		title := strings.TrimSpace(sec["title"])
		summary := truncateForArchitectureBundle(sec["summary"], 280)
		if title == "" || summary == "" {
			continue
		}
		out = append(out, map[string]string{
			"title":   title,
			"summary": summary,
		})
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func architectureDiagramCliffNotesHighlights(
	store knowledgepkg.KnowledgeStore,
	repoID string,
	audience knowledgepkg.Audience,
) []map[string]string {
	if store == nil || strings.TrimSpace(repoID) == "" {
		return nil
	}
	lookupOrder := []knowledgepkg.ArtifactKey{
		knowledgepkg.ArtifactKey{
			RepositoryID: repoID,
			Type:         knowledgepkg.ArtifactCliffNotes,
			Audience:     audience,
			Depth:        knowledgepkg.DepthMedium,
			Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		}.Normalized(),
		knowledgepkg.ArtifactKey{
			RepositoryID: repoID,
			Type:         knowledgepkg.ArtifactCliffNotes,
			Audience:     knowledgepkg.AudienceDeveloper,
			Depth:        knowledgepkg.DepthMedium,
			Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		}.Normalized(),
	}
	for _, key := range lookupOrder {
		artifact := store.GetArtifactByKey(key)
		if artifact == nil || artifact.Status != knowledgepkg.StatusReady {
			continue
		}
		sections := store.GetKnowledgeSections(artifact.ID)
		if len(sections) == 0 {
			continue
		}
		out := make([]map[string]string, 0, min(len(sections), 8))
		for _, sec := range sections {
			content := strings.TrimSpace(sec.Summary)
			if content == "" {
				content = strings.TrimSpace(sec.Content)
			}
			content = truncateForArchitectureBundle(content, 320)
			if content == "" {
				continue
			}
			out = append(out, map[string]string{
				"title":   sec.Title,
				"content": content,
			})
			if len(out) >= 8 {
				break
			}
		}
		return out
	}
	return nil
}

func truncateForArchitectureBundle(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return strings.TrimSpace(s[:limit]) + "..."
}

func cliffNotesDeepeningTargets(store knowledgepkg.KnowledgeStore, artifact *knowledgepkg.Artifact) []string {
	if store == nil || artifact == nil || artifact.Type != knowledgepkg.ArtifactCliffNotes {
		return nil
	}
	scopeType := knowledgepkg.ScopeRepository
	if artifact.Scope != nil {
		scopeType = artifact.Scope.Normalize().ScopeType
	}
	targets := knowledgepkg.DeepRefinementSectionTitles(scopeType)
	if len(targets) == 0 {
		return nil
	}
	current := store.GetKnowledgeSections(artifact.ID)
	byTitle := make(map[string]knowledgepkg.Section, len(current))
	for _, sec := range current {
		byTitle[sec.Title] = sec
	}
	refinementBySectionKey := make(map[string]knowledgepkg.RefinementUnit)
	for _, unit := range store.GetRefinementUnits(artifact.ID) {
		if unit.RefinementType != cliffNotesDeepRefinementType {
			continue
		}
		refinementBySectionKey[unit.SectionKey] = unit
	}
	var pending []string
	for _, title := range targets {
		sec, ok := byTitle[title]
		if !ok || strings.EqualFold(sec.RefinementStatus, "deep") {
			continue
		}
		if unit, ok := refinementBySectionKey[sec.SectionKey]; ok {
			switch unit.Status {
			case knowledgepkg.RefinementQueued, knowledgepkg.RefinementRunning, knowledgepkg.RefinementCompleted:
				continue
			}
		}
		pending = append(pending, title)
	}
	seen := make(map[string]struct{}, len(pending))
	for _, title := range pending {
		seen[title] = struct{}{}
	}
	for _, sec := range current {
		if sec.Title == "" || strings.EqualFold(sec.RefinementStatus, "deep") {
			continue
		}
		if _, ok := seen[sec.Title]; ok {
			continue
		}
		if sec.Confidence == knowledgepkg.ConfidenceLow || sec.Inferred {
			if unit, ok := refinementBySectionKey[sec.SectionKey]; ok {
				switch unit.Status {
				case knowledgepkg.RefinementQueued, knowledgepkg.RefinementRunning, knowledgepkg.RefinementCompleted:
					continue
				}
			}
			pending = append(pending, sec.Title)
			seen[sec.Title] = struct{}{}
		}
	}
	return pending
}

const cliffNotesDeepRefinementType = "cliff_notes_deep"
const cliffNotesLightRefinementType = "cliff_notes_light"

func syncCliffNotesRefinementUnits(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	sections []knowledgepkg.Section,
	understanding *knowledgepkg.RepositoryUnderstanding,
) {
	if store == nil || artifact == nil || artifact.Type != knowledgepkg.ArtifactCliffNotes || len(sections) == 0 {
		return
	}
	units := make([]knowledgepkg.RefinementUnit, 0, len(sections))
	for _, sec := range sections {
		sectionKey := sec.SectionKey
		if sectionKey == "" {
			sectionKey = knowledgepkg.SectionKeyForTitle(sec.Title)
		}
		units = append(units, knowledgepkg.RefinementUnit{
			ArtifactID:         artifact.ID,
			SectionKey:         sectionKey,
			SectionTitle:       sec.Title,
			RefinementType:     cliffNotesLightRefinementType,
			Status:             knowledgepkg.RefinementCompleted,
			UnderstandingID:    artifact.UnderstandingID,
			EvidenceRevisionFP: artifact.UnderstandingRevisionFP,
			RendererVersion:    knowledgepkg.RendererVersionForArtifact(artifact.Type),
			Metadata:           cliffNotesSectionMetadataJSON(knowledgepkg.ArtifactCliffNotes, understanding, "light", sec.Title, len(sec.Evidence) > 0),
		})
	}
	_ = store.StoreRefinementUnits(artifact.ID, units)
}

func markCliffNotesDeepRefinementStatus(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	sections []knowledgepkg.Section,
	selectedTitles []string,
	status knowledgepkg.RefinementStatus,
	lastError string,
) {
	if store == nil || artifact == nil || artifact.Type != knowledgepkg.ArtifactCliffNotes || len(selectedTitles) == 0 {
		return
	}
	byTitle := make(map[string]knowledgepkg.Section, len(sections))
	for _, sec := range sections {
		byTitle[sec.Title] = sec
	}
	existingAttempts := make(map[string]int)
	for _, unit := range store.GetRefinementUnits(artifact.ID) {
		if unit.RefinementType != cliffNotesDeepRefinementType {
			continue
		}
		existingAttempts[unit.SectionKey] = unit.AttemptCount
	}
	units := make([]knowledgepkg.RefinementUnit, 0, len(selectedTitles))
	for _, title := range selectedTitles {
		sec, ok := byTitle[title]
		if !ok {
			continue
		}
		sectionKey := sec.SectionKey
		if sectionKey == "" {
			sectionKey = knowledgepkg.SectionKeyForTitle(sec.Title)
		}
		attempts := existingAttempts[sectionKey]
		if status == knowledgepkg.RefinementRunning {
			attempts++
		}
		units = append(units, knowledgepkg.RefinementUnit{
			ArtifactID:         artifact.ID,
			SectionKey:         sectionKey,
			SectionTitle:       sec.Title,
			RefinementType:     cliffNotesDeepRefinementType,
			Status:             status,
			AttemptCount:       attempts,
			UnderstandingID:    artifact.UnderstandingID,
			EvidenceRevisionFP: artifact.UnderstandingRevisionFP,
			RendererVersion:    knowledgepkg.RendererVersionForArtifact(artifact.Type),
			LastError:          strings.TrimSpace(lastError),
		})
	}
	_ = store.StoreRefinementUnits(artifact.ID, units)
}

func understandingScopeForArtifact(scope knowledgepkg.ArtifactScope) knowledgepkg.ArtifactScope {
	return scope.Normalize()
}

func updateUnderstandingForCliffNotes(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
	resp *knowledgev1.GenerateCliffNotesResponse,
	stage knowledgepkg.RepositoryUnderstandingStage,
) (*knowledgepkg.RepositoryUnderstanding, error) {
	if store == nil || resp == nil {
		return nil, nil
	}
	if artifact == nil {
		return nil, fmt.Errorf("artifact is required")
	}
	understanding := &knowledgepkg.RepositoryUnderstanding{
		RepositoryID: artifact.RepositoryID,
		Scope:        understandingScopeForArtifact(scope).NormalizePtr(),
		RevisionFP:   knowledgepkg.RevisionFingerprint(sourceRevision),
		Stage:        stage,
		TreeStatus:   knowledgepkg.UnderstandingTreeMissing,
	}
	if resp.Diagnostics != nil {
		understanding.CorpusID = resp.Diagnostics.CorpusId
		if resp.Diagnostics.RevisionFp != "" {
			understanding.RevisionFP = resp.Diagnostics.RevisionFp
		}
		understanding.Strategy = resp.Diagnostics.Strategy
		understanding.CachedNodes = int(resp.Diagnostics.CachedNodes)
		understanding.TotalNodes = int(resp.Diagnostics.TotalNodes)
		understanding.ModelUsed = resp.Diagnostics.ModelUsed
		switch {
		case resp.Diagnostics.TotalNodes > 0:
			understanding.TreeStatus = knowledgepkg.UnderstandingTreeComplete
		case resp.Diagnostics.CachedNodes > 0:
			understanding.TreeStatus = knowledgepkg.UnderstandingTreePartial
		}
	}
	existing := store.GetRepositoryUnderstanding(artifact.RepositoryID, understandingScopeForArtifact(scope))
	if metadata := cliffNotesUnderstandingMetadata(existing, resp); metadata != "" {
		understanding.Metadata = metadata
	}
	if understanding.Strategy == "" {
		understanding.Strategy = "hierarchical"
	}
	if understanding.ModelUsed == "" && resp.Usage != nil {
		understanding.ModelUsed = resp.Usage.Model
	}
	stored, err := store.StoreRepositoryUnderstanding(understanding)
	if err != nil {
		return nil, err
	}
	if stored != nil && artifact.ID != "" {
		_ = store.AttachArtifactUnderstanding(artifact.ID, stored.ID, stored.RevisionFP)
		artifact.UnderstandingID = stored.ID
		artifact.UnderstandingRevisionFP = stored.RevisionFP
		_ = store.StoreArtifactDependencies(artifact.ID, []knowledgepkg.ArtifactDependency{{
			ArtifactID:       artifact.ID,
			DependencyType:   knowledgepkg.DependencyRepositoryUnderstanding,
			TargetID:         stored.ID,
			TargetRevisionFP: stored.RevisionFP,
			Metadata:         `{"source":"cliff_notes"}`,
		}})
	}
	return stored, nil
}

func cliffNotesUnderstandingMetadata(existing *knowledgepkg.RepositoryUnderstanding, resp *knowledgev1.GenerateCliffNotesResponse) string {
	meta := repositoryUnderstandingMetadata{}
	if existing != nil && strings.TrimSpace(existing.Metadata) != "" {
		_ = json.Unmarshal([]byte(existing.Metadata), &meta)
	}
	if resp == nil || len(resp.Sections) == 0 {
		if len(meta.FirstPassSections) == 0 && len(meta.Resume) == 0 {
			return ""
		}
		raw, err := json.Marshal(meta)
		if err != nil {
			return ""
		}
		return string(raw)
	}
	meta.FirstPassSections = make([]map[string]string, 0, len(resp.Sections))
	for _, sec := range resp.Sections {
		summary := strings.TrimSpace(sec.Summary)
		if summary == "" {
			summary = strings.TrimSpace(sec.Content)
		}
		if len(summary) > 280 {
			summary = summary[:280]
		}
		meta.FirstPassSections = append(meta.FirstPassSections, map[string]string{
			"title":   sec.Title,
			"summary": summary,
		})
	}
	if len(meta.FirstPassSections) == 0 {
		if len(meta.Resume) == 0 {
			return ""
		}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(raw)
}

func cliffNotesSectionMetadataJSON(
	artifactType knowledgepkg.ArtifactType,
	understanding *knowledgepkg.RepositoryUnderstanding,
	refinementTier string,
	sectionTitle string,
	hasEvidence bool,
) string {
	meta := cliffNotesSectionMetadata{
		SectionKey:          knowledgepkg.SectionKeyForTitle(sectionTitle),
		RefinementTier:      strings.TrimSpace(refinementTier),
		RefinedWithEvidence: hasEvidence,
		RendererVersion:     knowledgepkg.RendererVersionForArtifact(artifactType),
	}
	if understanding != nil {
		meta.EvidenceRevisionFP = strings.TrimSpace(understanding.RevisionFP)
		meta.UnderstandingID = strings.TrimSpace(understanding.ID)
	}
	if meta.SectionKey == "" &&
		meta.RefinementTier == "" &&
		!meta.RefinedWithEvidence &&
		meta.EvidenceRevisionFP == "" &&
		meta.RendererVersion == "" &&
		meta.UnderstandingID == "" {
		return ""
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(raw)
}

func seedRepositoryUnderstanding(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
	stage knowledgepkg.RepositoryUnderstandingStage,
) (*knowledgepkg.RepositoryUnderstanding, error) {
	if store == nil || artifact == nil {
		return nil, nil
	}
	u := &knowledgepkg.RepositoryUnderstanding{
		RepositoryID: artifact.RepositoryID,
		Scope:        understandingScopeForArtifact(scope).NormalizePtr(),
		RevisionFP:   knowledgepkg.RevisionFingerprint(sourceRevision),
		Stage:        stage,
		TreeStatus:   knowledgepkg.UnderstandingTreeMissing,
	}
	stored, err := store.StoreRepositoryUnderstanding(u)
	if err != nil {
		return nil, err
	}
	if stored != nil {
		_ = store.AttachArtifactUnderstanding(artifact.ID, stored.ID, stored.RevisionFP)
		artifact.UnderstandingID = stored.ID
		artifact.UnderstandingRevisionFP = stored.RevisionFP
		_ = store.StoreArtifactDependencies(artifact.ID, []knowledgepkg.ArtifactDependency{{
			ArtifactID:       artifact.ID,
			DependencyType:   knowledgepkg.DependencyRepositoryUnderstanding,
			TargetID:         stored.ID,
			TargetRevisionFP: stored.RevisionFP,
			Metadata:         `{"source":"seed"}`,
		}})
	}
	return stored, nil
}

func markRepositoryUnderstandingFailed(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
	err error,
) {
	if store == nil || artifact == nil {
		return
	}
	u := &knowledgepkg.RepositoryUnderstanding{
		RepositoryID: artifact.RepositoryID,
		Scope:        understandingScopeForArtifact(scope).NormalizePtr(),
		RevisionFP:   knowledgepkg.RevisionFingerprint(sourceRevision),
		Stage:        knowledgepkg.UnderstandingFailed,
		TreeStatus:   knowledgepkg.UnderstandingTreePartial,
	}
	if err != nil {
		u.ErrorMessage = err.Error()
	}
	stored, storeErr := store.StoreRepositoryUnderstanding(u)
	if storeErr == nil && stored != nil {
		_ = store.AttachArtifactUnderstanding(artifact.ID, stored.ID, stored.RevisionFP)
		artifact.UnderstandingID = stored.ID
		artifact.UnderstandingRevisionFP = stored.RevisionFP
		_ = store.StoreArtifactDependencies(artifact.ID, []knowledgepkg.ArtifactDependency{{
			ArtifactID:       artifact.ID,
			DependencyType:   knowledgepkg.DependencyRepositoryUnderstanding,
			TargetID:         stored.ID,
			TargetRevisionFP: stored.RevisionFP,
			Metadata:         `{"source":"failure"}`,
		}})
	}
}

func attachFreshUnderstanding(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
) (*knowledgepkg.RepositoryUnderstanding, bool) {
	if store == nil || artifact == nil {
		return nil, false
	}
	u := store.GetRepositoryUnderstanding(artifact.RepositoryID, understandingScopeForArtifact(scope))
	if u == nil {
		return nil, false
	}
	revisionFP := knowledgepkg.RevisionFingerprint(sourceRevision)
	if revisionFP == "" || u.RevisionFP == "" || revisionFP != u.RevisionFP {
		return u, false
	}
	_ = store.AttachArtifactUnderstanding(artifact.ID, u.ID, u.RevisionFP)
	artifact.UnderstandingID = u.ID
	artifact.UnderstandingRevisionFP = u.RevisionFP
	_ = store.StoreArtifactDependencies(artifact.ID, []knowledgepkg.ArtifactDependency{{
		ArtifactID:       artifact.ID,
		DependencyType:   knowledgepkg.DependencyRepositoryUnderstanding,
		TargetID:         u.ID,
		TargetRevisionFP: u.RevisionFP,
		Metadata:         `{"source":"reuse"}`,
	}})
	return u, u.TreeStatus == knowledgepkg.UnderstandingTreeComplete
}

func enrichSnapshotWithUnderstanding(snapshotJSON []byte, understanding *knowledgepkg.RepositoryUnderstanding) ([]byte, bool) {
	if understanding == nil || strings.TrimSpace(understanding.Metadata) == "" {
		return snapshotJSON, false
	}
	var meta repositoryUnderstandingMetadata
	if err := json.Unmarshal([]byte(understanding.Metadata), &meta); err != nil {
		return snapshotJSON, false
	}
	if len(meta.FirstPassSections) == 0 {
		return snapshotJSON, false
	}
	var snapMap map[string]any
	if err := json.Unmarshal(snapshotJSON, &snapMap); err != nil {
		return snapshotJSON, false
	}
	snapMap["_repository_understanding"] = meta
	enriched, err := json.Marshal(snapMap)
	if err != nil {
		return snapshotJSON, false
	}
	return enriched, true
}

func (r *Resolver) ensureFreshRepositoryUnderstanding(
	ctx context.Context,
	rt llm.Runtime,
	repo *graphstore.Repository,
	artifact *knowledgepkg.Artifact,
	sourceRevision knowledgepkg.SourceRevision,
	snapshotJSON []byte,
) (*knowledgepkg.RepositoryUnderstanding, bool, error) {
	if r == nil || r.KnowledgeStore == nil || r.Worker == nil || repo == nil || artifact == nil {
		return nil, false, nil
	}
	repoScope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
	// Accept any existing understanding (even if slightly stale) to avoid
	// blocking the worker with a concurrent cliff notes generation request.
	if understanding, fresh := attachFreshUnderstanding(r.KnowledgeStore, artifact, repoScope, sourceRevision); understanding != nil {
		if !fresh {
			slog.Info("using_stale_understanding",
				"artifact_id", artifact.ID,
				"understanding_id", understanding.ID)
		}
		return understanding, fresh, nil
	}

	// If cliff notes are already generating for this repo, skip the understanding
	// build rather than blocking the worker with a concurrent generation request.
	// The understanding will be available once the in-flight cliff notes complete.
	for _, existingCN := range r.KnowledgeStore.GetKnowledgeArtifacts(repo.ID) {
		if existingCN.Type != knowledgepkg.ArtifactCliffNotes || existingCN.Scope.ScopeType != knowledgepkg.ScopeRepository {
			continue
		}
		if existingCN.Status == knowledgepkg.StatusGenerating || existingCN.Status == knowledgepkg.StatusPending {
			slog.Info("skipping_understanding_build",
				"artifact_id", artifact.ID,
				"reason", "cliff_notes_already_generating",
				"cliff_notes_id", existingCN.ID)
			if rt != nil {
				rt.ReportProgress(0.12, "understanding", "Cliff notes in progress — proceeding without understanding")
			}
			return nil, false, nil
		}
		break
	}
	if _, err := seedRepositoryUnderstanding(r.KnowledgeStore, artifact, repoScope, sourceRevision, knowledgepkg.UnderstandingBuildingTree); err != nil {
		slog.Warn("failed to seed repository understanding", "artifact_id", artifact.ID, "error", err)
	}
	if rt != nil {
		rt.ReportProgress(0.12, "understanding", "Building repository understanding")
	}
	_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.12, "understanding", "Building repository understanding")
	resp, err := r.Worker.GenerateCliffNotes(
		r.withJobMetadata(ctx, "knowledge", rt, repo.ID, artifact.ID, "build_repository_understanding"),
		&knowledgev1.GenerateCliffNotesRequest{
			RepositoryId:   repo.ID,
			RepositoryName: repo.Name,
			Audience:       string(knowledgepkg.AudienceDeveloper),
			Depth:          string(knowledgepkg.DepthMedium),
			ScopeType:      string(knowledgepkg.ScopeRepository),
			SnapshotJson:   string(snapshotJSON),
		},
	)
	if err != nil {
		markRepositoryUnderstandingFailed(r.KnowledgeStore, artifact, repoScope, sourceRevision, err)
		return nil, false, err
	}
	understanding, err := updateUnderstandingForCliffNotes(
		r.KnowledgeStore,
		artifact,
		repoScope,
		sourceRevision,
		resp,
		knowledgepkg.UnderstandingFirstPassReady,
	)
	if err != nil {
		return nil, false, err
	}
	return understanding, false, nil
}

func knowledgeAudienceValue(audience *KnowledgeAudience) knowledgepkg.Audience {
	if audience == nil {
		return knowledgepkg.AudienceDeveloper
	}
	return knowledgepkg.Audience(strings.ToLower(string(*audience)))
}

func knowledgeDepthValue(depth *KnowledgeDepth) knowledgepkg.Depth {
	if depth == nil {
		return knowledgepkg.DepthMedium
	}
	return knowledgepkg.Depth(strings.ToLower(string(*depth)))
}

func knowledgeGenerationModeValue(mode *KnowledgeGenerationMode) knowledgepkg.GenerationMode {
	if mode == nil {
		return knowledgepkg.GenerationModeUnderstandingFirst
	}
	switch *mode {
	case KnowledgeGenerationModeClassic:
		return knowledgepkg.GenerationModeClassic
	default:
		return knowledgepkg.GenerationModeUnderstandingFirst
	}
}

func configuredKnowledgeGenerationModeDefault(store comprehension.Store) knowledgepkg.GenerationMode {
	if store != nil {
		if eff, err := comprehension.Resolve(store, comprehension.WorkspaceScope); err == nil && eff != nil {
			switch strings.TrimSpace(strings.ToLower(eff.KnowledgeGenerationModeDefault)) {
			case "classic":
				return knowledgepkg.GenerationModeClassic
			case "understanding_first":
				return knowledgepkg.GenerationModeUnderstandingFirst
			}
		}
	}
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("SOURCEBRIDGE_KNOWLEDGE_GENERATION_MODE_DEFAULT")))
	switch raw {
	case "classic":
		return knowledgepkg.GenerationModeClassic
	default:
		return knowledgepkg.GenerationModeUnderstandingFirst
	}
}

func resolvedKnowledgeGenerationMode(store comprehension.Store, repo *graphstore.Repository, requested *KnowledgeGenerationMode) knowledgepkg.GenerationMode {
	if requested != nil {
		return knowledgeGenerationModeValue(requested)
	}
	if repo != nil {
		switch strings.ToLower(strings.TrimSpace(repo.GenerationModeDefault)) {
		case "classic":
			return knowledgepkg.GenerationModeClassic
		case "understanding_first":
			return knowledgepkg.GenerationModeUnderstandingFirst
		}
	}
	return configuredKnowledgeGenerationModeDefault(store)
}

func knowledgePrewarmOnIndexEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("SOURCEBRIDGE_KNOWLEDGE_PREWARM_ON_INDEX")))
	switch raw {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

func enrichSnapshotWithCliffNotesAnalysis(
	store knowledgepkg.KnowledgeStore,
	repoID string,
	audience knowledgepkg.Audience,
	snapshotJSON []byte,
) ([]byte, bool) {
	if store == nil || strings.TrimSpace(repoID) == "" || len(snapshotJSON) == 0 {
		return snapshotJSON, false
	}
	lookupOrder := []knowledgepkg.ArtifactKey{
		knowledgepkg.ArtifactKey{
			RepositoryID: repoID,
			Type:         knowledgepkg.ArtifactCliffNotes,
			Audience:     audience,
			Depth:        knowledgepkg.DepthMedium,
			Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		}.Normalized(),
		knowledgepkg.ArtifactKey{
			RepositoryID: repoID,
			Type:         knowledgepkg.ArtifactCliffNotes,
			Audience:     knowledgepkg.AudienceDeveloper,
			Depth:        knowledgepkg.DepthMedium,
			Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		}.Normalized(),
	}
	var cliffNotes *knowledgepkg.Artifact
	for _, key := range lookupOrder {
		if candidate := store.GetArtifactByKey(key); candidate != nil && candidate.Status == knowledgepkg.StatusReady {
			cliffNotes = candidate
			break
		}
	}
	if cliffNotes == nil {
		return snapshotJSON, false
	}
	sections := store.GetKnowledgeSections(cliffNotes.ID)
	if len(sections) == 0 {
		return snapshotJSON, false
	}
	var snapMap map[string]any
	if err := json.Unmarshal(snapshotJSON, &snapMap); err != nil {
		return snapshotJSON, false
	}
	if _, exists := snapMap["_pre_analysis"]; exists {
		return snapshotJSON, false
	}
	analysis := make([]map[string]string, 0, len(sections))
	for _, sec := range sections {
		analysis = append(analysis, map[string]string{
			"title":   sec.Title,
			"content": sec.Content,
			"summary": sec.Summary,
		})
	}
	snapMap["_pre_analysis"] = analysis
	enriched, err := json.Marshal(snapMap)
	if err != nil {
		return snapshotJSON, false
	}
	return enriched, true
}

func artifactUsesUnderstanding(mode knowledgepkg.GenerationMode) bool {
	return mode != knowledgepkg.GenerationModeClassic
}

func syncArtifactExecutionMetadata(store knowledgepkg.KnowledgeStore, artifact *knowledgepkg.Artifact) {
	if store == nil || artifact == nil {
		return
	}
	artifact.RendererVersion = knowledgepkg.RendererVersionForArtifact(artifact.Type)
	if artifact.GenerationMode == "" {
		artifact.GenerationMode = knowledgepkg.GenerationModeUnderstandingFirst
	}
	_, _ = store.StoreKnowledgeArtifact(artifact)
}

func cliffNotesRenderPlanForArtifact(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	sourceRevision knowledgepkg.SourceRevision,
	understanding *knowledgepkg.RepositoryUnderstanding,
) cliffNotesRenderPlan {
	if store == nil || artifact == nil {
		return cliffNotesRenderPlan{}
	}
	if artifact.Type != knowledgepkg.ArtifactCliffNotes {
		return cliffNotesRenderPlan{}
	}
	if understanding == nil || artifact.UnderstandingRevisionFP == "" || understanding.RevisionFP == "" {
		return cliffNotesRenderPlan{}
	}
	if artifact.UnderstandingRevisionFP != understanding.RevisionFP {
		return cliffNotesRenderPlan{}
	}
	if rev := knowledgepkg.RevisionFingerprint(sourceRevision); rev != "" && understanding.RevisionFP != rev {
		return cliffNotesRenderPlan{}
	}
	scopeType := knowledgepkg.ScopeRepository
	if artifact.Scope != nil {
		scopeType = artifact.Scope.Normalize().ScopeType
	}
	required := knowledgepkg.RequiredCliffNotesSections(scopeType, artifact.Depth)
	existingSections := store.GetKnowledgeSections(artifact.ID)
	missing := knowledgepkg.MissingSectionTitles(existingSections, required)
	if artifact.RendererVersion != knowledgepkg.RendererVersionForArtifact(artifact.Type) {
		return cliffNotesRenderPlan{
			RenderOnly:         true,
			UnderstandingDepth: string(knowledgepkg.DepthMedium),
			RelevanceProfile:   "product_core",
		}
	}
	if len(missing) > 0 {
		return cliffNotesRenderPlan{
			RenderOnly:            true,
			SelectedSectionTitles: missing,
			UnderstandingDepth:    string(knowledgepkg.DepthMedium),
			RelevanceProfile:      "product_core",
		}
	}
	return cliffNotesRenderPlan{}
}

func artifactScopeFromInput(scopeType *KnowledgeScopeType, scopePath *string) (knowledgepkg.ArtifactScope, error) {
	scope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
	if scopeType != nil {
		switch *scopeType {
		case KnowledgeScopeTypeModule:
			scope.ScopeType = knowledgepkg.ScopeModule
		case KnowledgeScopeTypeFile:
			scope.ScopeType = knowledgepkg.ScopeFile
		case KnowledgeScopeTypeSymbol:
			scope.ScopeType = knowledgepkg.ScopeSymbol
		case KnowledgeScopeTypeRequirement:
			scope.ScopeType = knowledgepkg.ScopeRequirement
		default:
			scope.ScopeType = knowledgepkg.ScopeRepository
		}
	}
	if scopePath != nil {
		scope.ScopePath = *scopePath
	}
	scope = scope.Normalize()
	if scope.ScopeType != knowledgepkg.ScopeRepository && scope.ScopePath == "" {
		return knowledgepkg.ArtifactScope{}, fmt.Errorf("scopePath is required for non-repository scopes")
	}
	return scope, nil
}

func artifactKeyFromCliffNotesInput(input GenerateCliffNotesInput) (knowledgepkg.ArtifactKey, error) {
	scope, err := artifactScopeFromInput(input.ScopeType, input.ScopePath)
	if err != nil {
		return knowledgepkg.ArtifactKey{}, err
	}
	return knowledgepkg.ArtifactKey{
		RepositoryID: input.RepositoryID,
		Type:         knowledgepkg.ArtifactCliffNotes,
		Audience:     knowledgeAudienceValue(input.Audience),
		Depth:        knowledgeDepthValue(input.Depth),
		Scope:        scope,
	}.Normalized(), nil
}

func artifactKeyFromWorkflowStoryInput(input GenerateWorkflowStoryInput) (knowledgepkg.ArtifactKey, error) {
	scope, err := artifactScopeFromInput(input.ScopeType, input.ScopePath)
	if err != nil {
		return knowledgepkg.ArtifactKey{}, err
	}
	return knowledgepkg.ArtifactKey{
		RepositoryID: input.RepositoryID,
		Type:         knowledgepkg.ArtifactWorkflowStory,
		Audience:     knowledgeAudienceValue(input.Audience),
		Depth:        knowledgeDepthValue(input.Depth),
		Scope:        scope,
	}.Normalized(), nil
}

func scopeTypeToGraph(scopeType KnowledgeScopeType) knowledgepkg.ScopeType {
	switch scopeType {
	case KnowledgeScopeTypeModule:
		return knowledgepkg.ScopeModule
	case KnowledgeScopeTypeFile:
		return knowledgepkg.ScopeFile
	case KnowledgeScopeTypeSymbol:
		return knowledgepkg.ScopeSymbol
	case KnowledgeScopeTypeRequirement:
		return knowledgepkg.ScopeRequirement
	default:
		return knowledgepkg.ScopeRepository
	}
}

func buildScopeChildren(store graphstore.GraphStore, repoID string, scope knowledgepkg.ArtifactScope) []knowledgepkg.ArtifactScope {
	scope = scope.Normalize()
	switch scope.ScopeType {
	case knowledgepkg.ScopeRepository:
		return topLevelModuleScopes(store, repoID)
	case knowledgepkg.ScopeModule:
		return moduleChildScopes(store, repoID, scope.ScopePath)
	case knowledgepkg.ScopeFile:
		return fileChildScopes(store, repoID, scope.ScopePath)
	case knowledgepkg.ScopeRequirement:
		return []knowledgepkg.ArtifactScope{}
	default:
		return []knowledgepkg.ArtifactScope{}
	}
}

func topLevelModuleScopes(store graphstore.GraphStore, repoID string) []knowledgepkg.ArtifactScope {
	modules := store.GetModules(repoID)
	seen := map[string]bool{}
	var results []knowledgepkg.ArtifactScope
	for _, mod := range modules {
		root := strings.Split(strings.Trim(mod.Path, "/"), "/")[0]
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeModule,
			ScopePath: root,
		}.Normalize())
	}
	if len(results) > 0 {
		sort.Slice(results, func(i, j int) bool { return results[i].ScopePath < results[j].ScopePath })
		return results
	}

	topLevelFiles := map[string]bool{}
	for _, file := range store.GetFiles(repoID) {
		dir := path.Dir(file.Path)
		if dir == "." || dir == "" {
			topLevelFiles[file.Path] = true
			continue
		}
		root := strings.Split(strings.Trim(dir, "/"), "/")[0]
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeModule,
			ScopePath: root,
		}.Normalize())
	}
	for filePath := range topLevelFiles {
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeFile,
			ScopePath: filePath,
		}.Normalize())
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ScopePath < results[j].ScopePath })
	return results
}

func moduleChildScopes(store graphstore.GraphStore, repoID, modulePath string) []knowledgepkg.ArtifactScope {
	modulePath = strings.Trim(modulePath, "/")
	childModules := map[string]bool{}
	var results []knowledgepkg.ArtifactScope
	for _, mod := range store.GetModules(repoID) {
		trimmed := strings.Trim(mod.Path, "/")
		if trimmed == modulePath || !strings.HasPrefix(trimmed, modulePath+"/") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, modulePath+"/")
		if rest == "" {
			continue
		}
		if part, _, found := strings.Cut(rest, "/"); found {
			childPath := modulePath + "/" + part
			if !childModules[childPath] {
				childModules[childPath] = true
				results = append(results, knowledgepkg.ArtifactScope{
					ScopeType: knowledgepkg.ScopeModule,
					ScopePath: childPath,
				}.Normalize())
			}
		}
	}
	for _, file := range store.GetFiles(repoID) {
		dir := path.Dir(file.Path)
		if dir == "." {
			dir = ""
		}
		if dir != modulePath {
			continue
		}
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeFile,
			ScopePath: file.Path,
		}.Normalize())
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ScopePath < results[j].ScopePath })
	return results
}

func fileChildScopes(store graphstore.GraphStore, repoID, filePath string) []knowledgepkg.ArtifactScope {
	symbols := store.GetSymbolsByFile(repoID, filePath)
	results := make([]knowledgepkg.ArtifactScope, 0, len(symbols))
	for _, sym := range symbols {
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeSymbol,
			ScopePath: filePath + "#" + sym.Name,
		}.Normalize())
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ScopePath < results[j].ScopePath })
	return results
}

func scopeLabel(scope knowledgepkg.ArtifactScope) string {
	scope = scope.Normalize()
	switch scope.ScopeType {
	case knowledgepkg.ScopeModule:
		return scope.ScopePath + "/"
	case knowledgepkg.ScopeFile:
		return path.Base(scope.ScopePath)
	case knowledgepkg.ScopeSymbol:
		if scope.SymbolName != "" {
			return scope.SymbolName
		}
		_, symbol, _ := strings.Cut(scope.ScopePath, "#")
		return symbol
	case knowledgepkg.ScopeRequirement:
		if scope.SymbolName != "" {
			return scope.SymbolName
		}
		return scope.ScopePath
	default:
		return "Repository"
	}
}

func (r *Resolver) enqueueCliffNotesDeepening(
	repo *graphstore.Repository,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
	snapshotJSON []byte,
	selectedTitles []string,
) error {
	if r == nil || r.Orchestrator == nil || r.Worker == nil || r.KnowledgeStore == nil {
		return nil
	}
	if repo == nil || artifact == nil || len(selectedTitles) == 0 {
		return nil
	}
	if artifact.GenerationMode == knowledgepkg.GenerationModeClassic {
		return nil
	}
	currentSections := r.KnowledgeStore.GetKnowledgeSections(artifact.ID)
	markCliffNotesDeepRefinementStatus(r.KnowledgeStore, artifact, currentSections, selectedTitles, knowledgepkg.RefinementQueued, "")
	req := &llm.EnqueueRequest{
		Subsystem:      llm.SubsystemKnowledge,
		JobType:        "cliff_notes_deepen",
		TargetKey:      fmt.Sprintf("refine:%s:%s", artifact.ID, strings.Join(selectedTitles, "|")),
		Strategy:       "knowledge_artifact_refinement",
		ArtifactID:     artifact.ID,
		RepoID:         repo.ID,
		Priority:       llm.PriorityMaintenance,
		GenerationMode: string(artifact.GenerationMode),
		MaxAttempts:    1,
		RunWithContext: func(runCtx context.Context, rt llm.Runtime) error {
			rt.ReportProgress(0.05, "deepening", "Deepening critical cliff note sections")
			markCliffNotesDeepRefinementStatus(r.KnowledgeStore, artifact, r.KnowledgeStore.GetKnowledgeSections(artifact.ID), selectedTitles, knowledgepkg.RefinementRunning, "")
			bgCtx := r.withJobMetadata(runCtx, "knowledge", rt, repo.ID, artifact.ID, "cliff_notes_deepen")
			bgCtx = withCliffNotesRenderMetadata(bgCtx, true, selectedTitles, string(knowledgepkg.DepthMedium), "product_core")
			resp, err := r.Worker.GenerateCliffNotes(bgCtx, &knowledgev1.GenerateCliffNotesRequest{
				RepositoryId:   repo.ID,
				RepositoryName: repo.Name,
				Audience:       string(artifact.Audience),
				Depth:          string(knowledgepkg.DepthDeep),
				ScopeType:      string(scope.ScopeType),
				ScopePath:      scope.ScopePath,
				SnapshotJson:   string(snapshotJSON),
			})
			if err != nil {
				markCliffNotesDeepRefinementStatus(r.KnowledgeStore, artifact, r.KnowledgeStore.GetKnowledgeSections(artifact.ID), selectedTitles, knowledgepkg.RefinementFailed, err.Error())
				return err
			}
			incoming := make([]knowledgepkg.Section, 0, len(resp.Sections))
			for _, sec := range resp.Sections {
				incoming = append(incoming, knowledgepkg.Section{
					Title:            sec.Title,
					Content:          sec.Content,
					Summary:          sec.Summary,
					Metadata:         cliffNotesSectionMetadataJSON(knowledgepkg.ArtifactCliffNotes, &knowledgepkg.RepositoryUnderstanding{ID: artifact.UnderstandingID, RevisionFP: artifact.UnderstandingRevisionFP}, "deep", sec.Title, len(sec.Evidence) > 0),
					Confidence:       mapProtoConfidence(sec.Confidence),
					Inferred:         sec.Inferred,
					Evidence:         mapProtoEvidence(sec.Evidence),
					SectionKey:       knowledgepkg.SectionKeyForTitle(sec.Title),
					RefinementStatus: "deep",
				})
			}
			selected := make(map[string]struct{}, len(selectedTitles))
			for _, title := range selectedTitles {
				selected[title] = struct{}{}
			}
			merged := knowledgepkg.MergeSectionsByTitle(r.KnowledgeStore.GetKnowledgeSections(artifact.ID), incoming, selected)
			if err := r.KnowledgeStore.SupersedeArtifact(artifact.ID, merged); err != nil {
				markCliffNotesDeepRefinementStatus(r.KnowledgeStore, artifact, r.KnowledgeStore.GetKnowledgeSections(artifact.ID), selectedTitles, knowledgepkg.RefinementFailed, err.Error())
				return err
			}
			markCliffNotesDeepRefinementStatus(r.KnowledgeStore, artifact, merged, selectedTitles, knowledgepkg.RefinementCompleted, "")
			rt.ReportProgress(1.0, "ready", "Section deepening complete")
			return nil
		},
	}
	_, err := r.Orchestrator.Enqueue(req)
	return err
}

func mapProtoEvidence(evidence []*knowledgev1.KnowledgeEvidence) []knowledgepkg.Evidence {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]knowledgepkg.Evidence, len(evidence))
	for i, ev := range evidence {
		out[i] = knowledgepkg.Evidence{
			SourceType: knowledgepkg.EvidenceSourceType(ev.SourceType),
			SourceID:   ev.SourceId,
			FilePath:   ev.FilePath,
			LineStart:  int(ev.LineStart),
			LineEnd:    int(ev.LineEnd),
			Rationale:  ev.Rationale,
		}
	}
	return out
}
