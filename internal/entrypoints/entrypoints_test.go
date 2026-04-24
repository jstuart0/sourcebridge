// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package entrypoints

import "testing"

func TestClassify_BasicMain(t *testing.T) {
	syms := []Symbol{
		{ID: "1", Name: "main", Kind: "function", Language: "go", FilePath: "cmd/app/main.go", StartLine: 10, EndLine: 20},
	}
	got := Classify(syms, nil, PrecisionBasic)
	if len(got) != 1 || got[0].Kind != KindMain {
		t.Fatalf("expected one main entry, got %+v", got)
	}
	if got[0].Detector != "basic_main" {
		t.Errorf("unexpected detector %q", got[0].Detector)
	}
}

func TestClassify_BasicHTTPMatcher(t *testing.T) {
	syms := []Symbol{
		{ID: "1", Name: "getUserById", Kind: "function", Language: "typescript", FilePath: "src/users.ts", StartLine: 5, EndLine: 15},
		{ID: "2", Name: "postCreateOrder", Kind: "function", Language: "typescript", FilePath: "src/orders.ts", StartLine: 5, EndLine: 15},
		{ID: "3", Name: "randomHelper", Kind: "function", Language: "typescript", FilePath: "src/util.ts", StartLine: 1, EndLine: 5},
	}
	got := Classify(syms, nil, PrecisionBasic)
	if len(got) != 2 {
		t.Fatalf("expected 2 HTTP entries, got %d: %+v", len(got), got)
	}
	for _, ep := range got {
		if ep.Kind != KindHTTPRoute {
			t.Errorf("expected HTTPRoute, got %s for %s", ep.Kind, ep.SymbolName)
		}
		if ep.Detector != "basic_http_name" {
			t.Errorf("unexpected detector %q", ep.Detector)
		}
	}
}

func TestClassify_SkipsTests(t *testing.T) {
	syms := []Symbol{
		{ID: "1", Name: "TestMainThing", Kind: "function", Language: "go", FilePath: "main_test.go", StartLine: 1, EndLine: 10, IsTest: true},
		{ID: "2", Name: "main", Kind: "function", Language: "go", FilePath: "main.go", StartLine: 1, EndLine: 10},
	}
	got := Classify(syms, nil, PrecisionBasic)
	if len(got) != 1 || got[0].SymbolName != "main" {
		t.Errorf("expected only main (non-test), got %+v", got)
	}
}

func TestClassify_FrameworkAware_Grails(t *testing.T) {
	syms := []Symbol{
		{ID: "c1", Name: "index", Kind: "function", Language: "groovy", FilePath: "grails-app/controllers/FooController.groovy", StartLine: 5, EndLine: 10},
		{ID: "c2", Name: "show", Kind: "function", Language: "groovy", FilePath: "grails-app/controllers/FooController.groovy", StartLine: 12, EndLine: 20},
	}
	files := []File{
		{Path: "grails-app/controllers/FooController.groovy", Language: "groovy", GrailsRole: "grails_controller"},
	}
	got := Classify(syms, files, PrecisionFrameworkAware)
	if len(got) != 2 {
		t.Fatalf("expected 2 Grails actions, got %d", len(got))
	}
	for _, ep := range got {
		if ep.Kind != KindGrailsAction {
			t.Errorf("expected GrailsAction, got %s", ep.Kind)
		}
		if ep.Detector != "grails_controller" {
			t.Errorf("unexpected detector %q", ep.Detector)
		}
	}
}

func TestClassify_FrameworkAware_FastAPI(t *testing.T) {
	syms := []Symbol{
		{
			ID:        "p1",
			Name:      "read_item",
			Kind:      "function",
			Language:  "python",
			FilePath:  "app.py",
			StartLine: 20,
			EndLine:   28,
			Signature: `@app.get("/items/{item_id}")
def read_item(item_id: int):`,
		},
	}
	got := Classify(syms, nil, PrecisionFrameworkAware)
	if len(got) != 1 {
		t.Fatalf("expected 1 FastAPI route, got %+v", got)
	}
	if got[0].Kind != KindHTTPRoute || got[0].Detector != "fastapi_or_flask" {
		t.Errorf("unexpected kind/detector: %s/%s", got[0].Kind, got[0].Detector)
	}
	if got[0].Route != "/items/{item_id}" {
		t.Errorf("expected route /items/{item_id}, got %q", got[0].Route)
	}
}

func TestClassify_FrameworkAware_GoHTTPSignature(t *testing.T) {
	syms := []Symbol{
		{
			ID:        "g1",
			Name:      "ServeFiles",
			Kind:      "function",
			Language:  "go",
			FilePath:  "internal/server/handler.go",
			StartLine: 10,
			EndLine:   30,
			Signature: `func ServeFiles(w http.ResponseWriter, r *http.Request)`,
		},
	}
	got := Classify(syms, nil, PrecisionFrameworkAware)
	if len(got) != 1 || got[0].Detector != "go_http_signature" {
		t.Fatalf("expected go_http_signature detection, got %+v", got)
	}
}

func TestClassify_FrameworkAware_NextAPIRoute(t *testing.T) {
	syms := []Symbol{
		{ID: "n1", Name: "GET", Kind: "function", Language: "typescript", FilePath: "app/api/users/route.ts", StartLine: 1, EndLine: 5},
		{ID: "n2", Name: "handler", Kind: "function", Language: "javascript", FilePath: "pages/api/login.js", StartLine: 1, EndLine: 5},
		{ID: "n3", Name: "handler", Kind: "function", Language: "typescript", FilePath: "src/random/not-api.ts", StartLine: 1, EndLine: 5},
	}
	got := Classify(syms, nil, PrecisionFrameworkAware)
	// Expect 2 Next API handlers; the third file isn't under an API route.
	if len(got) != 2 {
		t.Fatalf("expected 2 Next API handlers, got %d: %+v", len(got), got)
	}
	for _, ep := range got {
		if ep.Detector != "nextjs_api_route" {
			t.Errorf("unexpected detector %q", ep.Detector)
		}
	}
}

func TestClassify_BasicModeSkipsFrameworkDetectors(t *testing.T) {
	// A python function with a FastAPI decorator should NOT be
	// classified when precision=basic — only framework_aware
	// picks that up.
	syms := []Symbol{
		{
			ID:        "p1",
			Name:      "read_item",
			Kind:      "function",
			Language:  "python",
			FilePath:  "app.py",
			StartLine: 20,
			EndLine:   28,
			Signature: `@app.get("/items/{item_id}")`,
		},
	}
	got := Classify(syms, nil, PrecisionBasic)
	if len(got) != 0 {
		t.Errorf("basic mode should NOT detect fastapi route, got %+v", got)
	}
}
