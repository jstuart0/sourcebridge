package graphql

import (
	"encoding/json"
	"strings"
	"testing"

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
			{Path: "cmd/api", Name: "api", FileCount: 3},
			{Path: "internal/service", Name: "service", FileCount: 4},
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
	scaffoldJSON := []byte(`{"level":"MODULE","mermaid_source":"flowchart TD\nA-->B","modules":[{"path":"cmd/api","file_paths":["cmd/api/main.go"],"outbound_paths":["internal/service"]}]}`)

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
}
