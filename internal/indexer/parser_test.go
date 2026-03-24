// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func fixtureDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "tests", "fixtures", "multi-lang-repo")
}

func readFixture(t *testing.T, relPath string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(fixtureDir(), relPath))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", relPath, err)
	}
	return content
}

func TestParseGoFile(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "go/main.go")

	result, err := parser.ParseFile(context.Background(), "go/main.go", "go", content)
	if err != nil {
		t.Fatal(err)
	}

	if result.Language != "go" {
		t.Errorf("expected language 'go', got %q", result.Language)
	}

	// Should find functions
	if len(result.Symbols) == 0 {
		t.Fatal("expected symbols in Go file")
	}

	funcNames := symbolNames(result.Symbols)
	t.Logf("Go symbols found: %v", funcNames)

	// main.go should have a main function
	if !containsName(funcNames, "main") {
		t.Error("expected to find 'main' function")
	}
}

func TestParseGoPaymentProcessor(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "go/payment/processor.go")

	result, err := parser.ParseFile(context.Background(), "go/payment/processor.go", "go", content)
	if err != nil {
		t.Fatal(err)
	}

	funcNames := symbolNames(result.Symbols)
	t.Logf("Payment processor symbols: %v", funcNames)

	// Should find processPayment or similar
	if len(result.Symbols) == 0 {
		t.Error("expected symbols in payment processor")
	}
}

func TestParsePythonFile(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "python/auth.py")

	result, err := parser.ParseFile(context.Background(), "python/auth.py", "python", content)
	if err != nil {
		t.Fatal(err)
	}

	funcNames := symbolNames(result.Symbols)
	t.Logf("Python symbols found: %v", funcNames)

	if len(result.Symbols) == 0 {
		t.Fatal("expected symbols in Python file")
	}
}

func TestParseTypeScriptFile(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "typescript/src/api.ts")

	result, err := parser.ParseFile(context.Background(), "typescript/src/api.ts", "typescript", content)
	if err != nil {
		t.Fatal(err)
	}

	funcNames := symbolNames(result.Symbols)
	t.Logf("TypeScript symbols found: %v", funcNames)

	if len(result.Symbols) == 0 {
		t.Fatal("expected symbols in TypeScript file")
	}
}

func TestParseJavaFile(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "java/src/main/java/com/example/Service.java")

	result, err := parser.ParseFile(context.Background(), "java/src/main/java/com/example/Service.java", "java", content)
	if err != nil {
		t.Fatal(err)
	}

	funcNames := symbolNames(result.Symbols)
	t.Logf("Java symbols found: %v", funcNames)

	if len(result.Symbols) == 0 {
		t.Fatal("expected symbols in Java file")
	}

	// Should find the Service class
	classNames := symbolsOfKind(result.Symbols, SymbolClass)
	if !containsName(classNames, "Service") {
		t.Error("expected to find 'Service' class")
	}
}

func TestParseRustFile(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "rust/src/lib.rs")

	result, err := parser.ParseFile(context.Background(), "rust/src/lib.rs", "rust", content)
	if err != nil {
		t.Fatal(err)
	}

	funcNames := symbolNames(result.Symbols)
	t.Logf("Rust symbols found: %v", funcNames)

	if len(result.Symbols) == 0 {
		t.Fatal("expected symbols in Rust file")
	}
}

func TestParsePythonTestFile(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "python/tests/test_auth.py")

	result, err := parser.ParseFile(context.Background(), "python/tests/test_auth.py", "python", content)
	if err != nil {
		t.Fatal(err)
	}

	// Test functions should be marked as tests
	testSymbols := 0
	for _, s := range result.Symbols {
		if s.IsTest {
			testSymbols++
		}
	}

	t.Logf("Test symbols found: %d of %d", testSymbols, len(result.Symbols))

	if testSymbols == 0 {
		t.Error("expected test symbols to be marked in test file")
	}
}

func TestParseUnsupportedLanguage(t *testing.T) {
	parser := NewParser()
	content := []byte("some content")

	result, err := parser.ParseFile(context.Background(), "file.unknown", "unknown", content)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Errors) == 0 {
		t.Error("expected error for unsupported language")
	}
}

func TestAllTier1LanguagesParse(t *testing.T) {
	parser := NewParser()
	fixtures := map[string]string{
		"go":         "go/main.go",
		"python":     "python/auth.py",
		"typescript": "typescript/src/api.ts",
		"java":       "java/src/main/java/com/example/Service.java",
		"rust":       "rust/src/lib.rs",
	}

	totalSymbols := 0
	for lang, path := range fixtures {
		content := readFixture(t, path)
		result, err := parser.ParseFile(context.Background(), path, lang, content)
		if err != nil {
			t.Errorf("failed to parse %s (%s): %v", path, lang, err)
			continue
		}
		t.Logf("%s (%s): %d symbols, %d imports", path, lang, len(result.Symbols), len(result.Imports))
		totalSymbols += len(result.Symbols)
	}

	t.Logf("Total symbols across all languages: %d", totalSymbols)
	if totalSymbols < 15 {
		t.Errorf("expected at least 15 total symbols across all Tier 1 languages, got %d", totalSymbols)
	}
}

func TestExtractModules(t *testing.T) {
	files := []FileResult{
		{Path: "go/main.go"},
		{Path: "go/payment/processor.go"},
		{Path: "python/auth.py"},
		{Path: "python/tests/test_auth.py"},
		{Path: "typescript/src/api.ts"},
	}

	modules := ExtractModules(files)
	if len(modules) == 0 {
		t.Fatal("expected modules to be extracted")
	}

	t.Logf("Extracted %d modules", len(modules))
	for _, m := range modules {
		t.Logf("  Module: %s (path=%s, files=%d)", m.Name, m.Path, m.FileCount)
	}
}

func TestLanguageRegistry(t *testing.T) {
	expected := []string{"go", "python", "typescript", "javascript", "java", "rust"}
	for _, lang := range expected {
		config := GetLanguageConfig(lang)
		if config == nil {
			t.Errorf("expected language %q to be registered", lang)
		}
	}

	if cfg := GetLanguageConfig("nonexistent"); cfg != nil {
		t.Error("expected nil for nonexistent language")
	}
}

// Helpers

func symbolNames(symbols []Symbol) []string {
	names := make([]string, len(symbols))
	for i, s := range symbols {
		names[i] = s.Name
	}
	return names
}

func symbolsOfKind(symbols []Symbol, kind SymbolKind) []string {
	var names []string
	for _, s := range symbols {
		if s.Kind == kind {
			names = append(names, s.Name)
		}
	}
	return names
}

func containsName(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Call extraction tests
// ---------------------------------------------------------------------------

func TestExtractCallsGo(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "go/main.go")

	result, err := parser.ParseFile(context.Background(), "go/main.go", "go", content)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Calls) == 0 {
		t.Fatal("expected call sites in Go file")
	}

	calleeNames := make(map[string]bool)
	for _, c := range result.Calls {
		calleeNames[c.CalleeName] = true
		t.Logf("Call: %s (caller=%s, line=%d)", c.CalleeName, c.CallerID, c.Line)
	}

	// main.go has: main() calls NewConfig(), StartServer(), Validate(), Printf()
	for _, expected := range []string{"NewConfig", "StartServer"} {
		if !calleeNames[expected] {
			t.Errorf("expected call to %q", expected)
		}
	}
}

func TestExtractCallsPaymentProcessor(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "go/payment/processor.go")

	result, err := parser.ParseFile(context.Background(), "go/payment/processor.go", "go", content)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Calls) == 0 {
		t.Fatal("expected call sites in payment processor")
	}

	calleeNames := make(map[string]bool)
	for _, c := range result.Calls {
		calleeNames[c.CalleeName] = true
	}

	// ProcessPayment calls validate(), requireApproval(), charge()
	for _, expected := range []string{"validate", "requireApproval", "charge"} {
		if !calleeNames[expected] {
			t.Errorf("expected call to %q in ProcessPayment", expected)
		}
	}
}

func TestExtractCallsPython(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "python/auth.py")

	result, err := parser.ParseFile(context.Background(), "python/auth.py", "python", content)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Calls) == 0 {
		t.Fatal("expected call sites in Python file")
	}

	calleeNames := make(map[string]bool)
	for _, c := range result.Calls {
		calleeNames[c.CalleeName] = true
	}

	// AuthService methods call hash_password, verify_password, etc.
	if !calleeNames["hash_password"] {
		t.Error("expected call to hash_password")
	}
}

func TestExtractCallsTypeScript(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "typescript/src/api.ts")

	result, err := parser.ParseFile(context.Background(), "typescript/src/api.ts", "typescript", content)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Calls) == 0 {
		t.Fatal("expected call sites in TypeScript file")
	}

	t.Logf("TypeScript calls found: %d", len(result.Calls))
	for _, c := range result.Calls {
		t.Logf("  Call: %s (line=%d)", c.CalleeName, c.Line)
	}
}

// ---------------------------------------------------------------------------
// Doc comment tests
// ---------------------------------------------------------------------------

func TestDocCommentsGo(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "go/main.go")

	result, err := parser.ParseFile(context.Background(), "go/main.go", "go", content)
	if err != nil {
		t.Fatal(err)
	}

	docFound := false
	for _, sym := range result.Symbols {
		if sym.DocComment != "" {
			docFound = true
			t.Logf("Go doc comment for %s: %q", sym.Name, sym.DocComment)
		}
	}

	if !docFound {
		t.Error("expected at least one doc comment in Go file")
	}

	// NewConfig should have "NewConfig creates a default configuration"
	for _, sym := range result.Symbols {
		if sym.Name == "NewConfig" && sym.DocComment == "" {
			t.Error("expected doc comment for NewConfig")
		}
	}
}

func TestDocCommentsPython(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "python/auth.py")

	result, err := parser.ParseFile(context.Background(), "python/auth.py", "python", content)
	if err != nil {
		t.Fatal(err)
	}

	docFound := false
	for _, sym := range result.Symbols {
		if sym.DocComment != "" {
			docFound = true
			t.Logf("Python doc comment for %s: %q", sym.Name, sym.DocComment)
		}
	}

	if !docFound {
		t.Error("expected at least one docstring in Python file")
	}
}

func TestDocCommentsRust(t *testing.T) {
	parser := NewParser()
	content := readFixture(t, "rust/src/lib.rs")

	result, err := parser.ParseFile(context.Background(), "rust/src/lib.rs", "rust", content)
	if err != nil {
		t.Fatal(err)
	}

	docFound := false
	for _, sym := range result.Symbols {
		if sym.DocComment != "" {
			docFound = true
			t.Logf("Rust doc comment for %s: %q", sym.Name, sym.DocComment)
		}
	}

	if !docFound {
		t.Error("expected at least one doc comment in Rust file")
	}
}

// ---------------------------------------------------------------------------
// Scoped call resolution tests
// ---------------------------------------------------------------------------

func TestCallResolutionSameFile(t *testing.T) {
	indexer := NewIndexer(nil)
	content := readFixture(t, "go/payment/processor.go")

	result, err := indexer.parser.ParseFile(context.Background(), "go/payment/processor.go", "go", content)
	if err != nil {
		t.Fatal(err)
	}

	// Build a mini IndexResult with one file
	idxResult := &IndexResult{
		Files: []FileResult{*result},
	}

	relations := indexer.resolveCallGraph(idxResult)
	t.Logf("Resolved %d call relations", len(relations))

	if len(relations) == 0 {
		t.Error("expected at least one resolved call relation")
	}

	// ProcessPayment → validate should resolve (same file, unambiguous)
	found := false
	for _, rel := range relations {
		if rel.Type == RelationCalls {
			// Find the symbols
			var callerName, calleeName string
			for _, sym := range result.Symbols {
				if sym.ID == rel.SourceID {
					callerName = sym.Name
				}
				if sym.ID == rel.TargetID {
					calleeName = sym.Name
				}
			}
			t.Logf("  %s → %s", callerName, calleeName)
			if callerName == "ProcessPayment" && calleeName == "validate" {
				found = true
			}
		}
	}

	if !found {
		t.Error("expected ProcessPayment → validate call relation")
	}
}

func TestCallResolutionAmbiguousSkipped(t *testing.T) {
	// Create two files with same-named functions to test ambiguity skipping
	result := &IndexResult{
		Files: []FileResult{
			{
				Path:     "pkg/a/foo.go",
				Language: "go",
				Symbols: []Symbol{
					{ID: "caller-1", Name: "caller", Kind: SymbolFunction, FilePath: "pkg/a/foo.go", StartLine: 1, EndLine: 10},
					{ID: "helper-a", Name: "helper", Kind: SymbolFunction, FilePath: "pkg/a/foo.go", StartLine: 12, EndLine: 20},
				},
				Calls: []CallSite{
					{CallerID: "caller-1", CalleeName: "helper", FilePath: "pkg/a/foo.go", Line: 5},
				},
			},
			{
				Path:     "pkg/b/bar.go",
				Language: "go",
				Symbols: []Symbol{
					{ID: "helper-b", Name: "helper", Kind: SymbolFunction, FilePath: "pkg/b/bar.go", StartLine: 1, EndLine: 10},
				},
			},
		},
	}

	indexer := NewIndexer(nil)
	relations := indexer.resolveCallGraph(result)

	// Should resolve to helper-a (same file, unambiguous)
	if len(relations) != 1 {
		t.Fatalf("expected 1 relation (same-file match), got %d", len(relations))
	}
	if relations[0].TargetID != "helper-a" {
		t.Errorf("expected target helper-a, got %s", relations[0].TargetID)
	}
}
