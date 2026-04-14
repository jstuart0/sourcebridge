package graphql

import (
	"encoding/json"
	"strings"
	"testing"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

func TestBuildArchitectureDiagramPromptBundleBoundsLargeSnapshotContext(t *testing.T) {
	t.Helper()

	largeDoc := strings.Repeat("architecture details\n", 2000)
	snap := &knowledgepkg.KnowledgeSnapshot{
		RepositoryID:   "repo-1",
		RepositoryName: "Example Repo",
		SourceRevision: knowledgepkg.SourceRevision{ContentFingerprint: "abc123"},
		Languages: []knowledgepkg.LanguageSummary{
			{Language: "go", FileCount: 12, SymbolCount: 60},
		},
		Modules: []knowledgepkg.ModuleSummary{
			{Path: "internal/api", Name: "api", FileCount: 3},
			{Path: "internal/knowledge", Name: "knowledge", FileCount: 4},
			{Path: "workers", Name: "workers", FileCount: 6},
		},
		EntryPoints: []knowledgepkg.SymbolRef{
			{Name: "main", FilePath: "cmd/api/main.go"},
		},
		PublicAPI: []knowledgepkg.SymbolRef{
			{Name: "ServeHTTP", FilePath: "internal/api/http.go"},
		},
		HighFanOutSymbols: []knowledgepkg.SymbolRef{
			{Name: "Dispatch", FilePath: "internal/service/dispatch.go"},
		},
		Docs: []knowledgepkg.DocRef{
			{Path: "README.md", Content: largeDoc},
		},
	}
	understanding := &knowledgepkg.RepositoryUnderstanding{
		Metadata: `{"first_pass_sections":[{"title":"Architecture Overview","summary":"Layered web app with API, service, and storage modules."}]}`,
	}
	scaffoldJSON := []byte(`{"level":"MODULE","mermaid_source":"flowchart TD\nA-->B","modules":[{"path":"internal/api","file_paths":["internal/api/http.go"],"outbound_paths":["internal/knowledge"]},{"path":"internal/knowledge","file_paths":["internal/knowledge/engine.go"],"outbound_paths":["workers"]},{"path":"workers","file_paths":["workers/knowledge/servicer.py"],"outbound_paths":["internal/db","internal/graph","internal/git"]}]}`)

	raw, err := buildArchitectureDiagramPromptBundle(nil, "repo-1", knowledgepkg.AudienceDeveloper, snap, understanding, scaffoldJSON)
	if err != nil {
		t.Fatalf("buildArchitectureDiagramPromptBundle: %v", err)
	}
	if len(raw) >= len(largeDoc) {
		t.Fatalf("expected bounded bundle smaller than original doc payload, got %d >= %d", len(raw), len(largeDoc))
	}

	var bundle architectureDiagramPromptBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	if got := len(bundle.DocumentationHighlights); got != 1 {
		t.Fatalf("expected one documentation highlight, got %d", got)
	}
	if len(bundle.DocumentationHighlights[0].Content) >= len(largeDoc) {
		t.Fatal("expected documentation content to be truncated")
	}
	if len(bundle.RepositoryUnderstanding) != 1 {
		t.Fatalf("expected one repository understanding highlight, got %d", len(bundle.RepositoryUnderstanding))
	}
	if len(bundle.RepresentativeFiles) == 0 {
		t.Fatal("expected representative files")
	}
	if bundle.DeterministicScaffold.Level != "MODULE" {
		t.Fatalf("expected scaffold level MODULE, got %q", bundle.DeterministicScaffold.Level)
	}
	if len(bundle.SystemComponents) == 0 {
		t.Fatal("expected system components")
	}
	if len(bundle.SystemFlows) == 0 {
		t.Fatal("expected system flows")
	}
	for _, flow := range bundle.SystemFlows {
		if flow.Summary == "primary flow" || flow.Summary == "major flow" {
			t.Fatalf("expected semantic flow label, got %q", flow.Summary)
		}
	}
}

func TestArchitectureDiagramMetadataIncludesExecutionViewAndStrategy(t *testing.T) {
	bundle := architectureDiagramPromptBundle{
		SystemComponents: []architectureSystemComponent{
			{ID: "user_interfaces", Label: "User Interfaces"},
			{ID: "api_auth", Label: "API & Auth"},
			{ID: "knowledge_orchestration", Label: "Knowledge Orchestration"},
			{ID: "background_workers", Label: "Background Workers"},
			{ID: "code_graph_index", Label: "Code Graph & Index"},
			{ID: "repository_access", Label: "Repository Access"},
			{ID: "persistence", Label: "Persistence"},
			{ID: "llm_provider", Label: "LLM Provider"},
		},
	}
	resp := &knowledgev1.GenerateArchitectureDiagramResponse{
		MermaidSource:     "flowchart LR\napi-->worker",
		RawMermaidSource:  "flowchart LR\napi-->worker",
		ValidationStatus:  "repaired",
		RepairSummary:     "fell back to deterministic system view: invalid Mermaid",
		DiagramSummary:    "SourceBridge routes user requests through the interfaces and API, hands knowledge generation to the orchestration layer, executes jobs in background workers, grounds analysis in the code graph and repository understanding, persists artifacts and job state, and calls the configured LLM provider when synthesis is needed.",
		InferredEdges:     []string{"api -> worker"},
	}

	raw := architectureDiagramMetadataJSON(resp, &bundle)
	if raw == "" {
		t.Fatal("expected metadata JSON")
	}
	var meta architectureDiagramSectionMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta.GenerationStrategy != "fallback" {
		t.Fatalf("expected fallback generation strategy, got %q", meta.GenerationStrategy)
	}
	if meta.GraphAlignmentStatus != "inferred" {
		t.Fatalf("expected inferred graph alignment status, got %q", meta.GraphAlignmentStatus)
	}
	if len(meta.Components) != len(bundle.SystemComponents) {
		t.Fatalf("expected component grounding metadata, got %#v", meta.Components)
	}
	if !strings.Contains(meta.ExecutionMermaid, "subgraph request_path") {
		t.Fatalf("expected execution mermaid in metadata, got %q", meta.ExecutionMermaid)
	}
	if !strings.Contains(meta.ExecutionSummary, "background workers execute generation jobs") {
		t.Fatalf("unexpected execution summary %q", meta.ExecutionSummary)
	}
	if !strings.Contains(meta.SystemSummary, "SourceBridge routes user requests") {
		t.Fatalf("unexpected system summary %q", meta.SystemSummary)
	}
	if len(meta.InferredEdges) != 1 || meta.InferredEdges[0] != "api -> worker" {
		t.Fatalf("unexpected inferred edges %#v", meta.InferredEdges)
	}
}

func TestArchitectureDiagramMetadataFlagsContradictoryEdges(t *testing.T) {
	bundle := architectureDiagramPromptBundle{
		SystemFlows: []architectureSystemFlow{
			{SourceID: "api_auth", TargetID: "background_workers", Summary: "dispatches jobs"},
		},
	}
	resp := &knowledgev1.GenerateArchitectureDiagramResponse{
		ValidationStatus: "repaired",
		RepairSummary:    "flagged 1 graph-contradictory edges",
		InferredEdges:    []string{"background_workers -> api_auth"},
	}

	raw := architectureDiagramMetadataJSON(resp, &bundle)
	var meta architectureDiagramSectionMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta.GraphAlignmentStatus != "contradictory" {
		t.Fatalf("expected contradictory graph alignment status, got %q", meta.GraphAlignmentStatus)
	}
	if len(meta.ContradictoryEdges) != 1 || meta.ContradictoryEdges[0] != "background_workers -> api_auth" {
		t.Fatalf("unexpected contradictory edges %#v", meta.ContradictoryEdges)
	}
}
