package graphql

import (
	"testing"

	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

func TestRepositoryFieldGuideSeedFilesPrioritizesHighSignalFiles(t *testing.T) {
	files := repositoryFieldGuideSeedFiles(&knowledgepkg.KnowledgeSnapshot{
		EntryPoints: []knowledgepkg.SymbolRef{
			{FilePath: "cmd/server/main.go"},
		},
		PublicAPI: []knowledgepkg.SymbolRef{
			{FilePath: "internal/api/rest/auth.go"},
		},
		HighFanOutSymbols: []knowledgepkg.SymbolRef{
			{FilePath: "internal/api/graphql/schema.resolvers.go"},
		},
		ComplexSymbols: []knowledgepkg.SymbolRef{
			{FilePath: "web/src/app/repositories/page.tsx"},
			{FilePath: "internal/api/rest/auth_test.go"},
		},
	})

	if len(files) == 0 {
		t.Fatal("expected candidate files")
	}
	if files[0] != "cmd/server/main.go" {
		t.Fatalf("expected entry-point file first, got %q", files[0])
	}
	for _, file := range files {
		if file == "internal/api/rest/auth_test.go" {
			t.Fatal("did not expect test file to be seeded")
		}
	}
}
