// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package entrypoints classifies code symbols as runtime entry points:
// program mains, HTTP route handlers, CLI commands, message handlers,
// and scheduled jobs.
//
// Two modes:
//
//   PrecisionBasic         — language-agnostic heuristics over symbol
//                             names only. Catches main() / run() style
//                             entries and obvious HTTP matchers
//                             (handler names containing "Get"/"Post"
//                             + common router receivers).
//
//   PrecisionFrameworkAware — adds framework-specific detectors:
//                             Grails directory conventions (via
//                             FileResult.GrailsRole), Python
//                             decorator hints (FastAPI/Flask),
//                             Go chi/Gin route calls, Express/Next.
//
// The classifier takes stored symbols + files and returns a list of
// EntryPoint records. Callers (MCP handler) filter by kind and
// project into whatever shape they need.
package entrypoints

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Precision controls how deep the classifier digs. Basic is
// language-agnostic; FrameworkAware adds per-framework detectors.
type Precision string

const (
	PrecisionBasic          Precision = "basic"
	PrecisionFrameworkAware Precision = "framework_aware"
)

// Kind categorizes an entry point.
type Kind string

const (
	KindMain            Kind = "main"
	KindHTTPRoute       Kind = "http_route"
	KindCLICommand      Kind = "cli_command"
	KindMessageHandler  Kind = "message_handler"
	KindScheduledJob    Kind = "scheduled_job"
	KindGrailsAction    Kind = "grails_controller_action"
)

// Symbol is the subset of a stored symbol the classifier needs. The
// MCP layer adapts graph.StoredSymbol to this shape.
type Symbol struct {
	ID         string
	Name       string
	Kind       string // function, method, class, …
	Language   string
	FilePath   string
	StartLine  int
	EndLine    int
	Signature  string
	IsTest     bool
}

// File is the subset of a parsed file the classifier needs.
type File struct {
	Path       string
	Language   string
	GrailsRole string
}

// EntryPoint describes a detected entry point.
type EntryPoint struct {
	Kind       Kind   `json:"kind"`
	SymbolID   string `json:"symbol_id"`
	SymbolName string `json:"symbol_name"`
	FilePath   string `json:"file_path"`
	Language   string `json:"language"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	// Detector identifies which per-framework rule fired. Values are
	// stable public strings so clients can filter ("grails_controller",
	// "fastapi_route", etc.).
	Detector   string `json:"detector"`
	// Route is populated for HTTP/CLI entries where the classifier
	// could extract the route string. Empty when the detector only
	// knows the function is a handler but not its binding.
	Route      string `json:"route,omitempty"`
}

// Classify walks the symbols + files and returns detected entry
// points at the requested precision. The output is sorted
// deterministically (kind, file_path, start_line).
func Classify(symbols []Symbol, files []File, precision Precision) []EntryPoint {
	// Build a quick lookup from file path → File metadata.
	fileMeta := make(map[string]File, len(files))
	for _, f := range files {
		fileMeta[f.Path] = f
	}

	var out []EntryPoint
	for _, s := range symbols {
		if s.IsTest {
			continue
		}
		if ep := classifyBasic(s); ep != nil {
			out = append(out, *ep)
			continue
		}
		if precision == PrecisionFrameworkAware {
			if ep := classifyFrameworkAware(s, fileMeta[s.FilePath]); ep != nil {
				out = append(out, *ep)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		return out[i].StartLine < out[j].StartLine
	})
	return out
}

// ---------------------------------------------------------------------------
// Basic mode — language-agnostic
// ---------------------------------------------------------------------------

// reHTTPMatcher matches method-receiver patterns common across
// Express/Gin/chi/FastAPI-client code where the symbol name itself
// carries the HTTP verb. Catches identifiers like "getUserById",
// "postCreateOrder", "handlePOST", etc. False-positive rate is
// bounded; text-reference style.
var reHTTPMatcher = regexp.MustCompile(`(?i)(^|[._])(get|post|put|delete|patch|head|options)[A-Z_]`)

// reMainName matches things that look like program mains across the
// language set SourceBridge indexes.
var reMainName = regexp.MustCompile(`^(main|Main|run|Run|entry|Entry|start|Start)$`)

func classifyBasic(s Symbol) *EntryPoint {
	// Skip class-like kinds from basic HTTP detection — we only
	// classify functions/methods as entry points.
	kindFunction := s.Kind == "function" || s.Kind == "method"

	if kindFunction && reMainName.MatchString(s.Name) {
		return &EntryPoint{
			Kind:       KindMain,
			SymbolID:   s.ID,
			SymbolName: s.Name,
			FilePath:   s.FilePath,
			Language:   s.Language,
			StartLine:  s.StartLine,
			EndLine:    s.EndLine,
			Detector:   "basic_main",
		}
	}

	if kindFunction && reHTTPMatcher.MatchString(s.Name) {
		return &EntryPoint{
			Kind:       KindHTTPRoute,
			SymbolID:   s.ID,
			SymbolName: s.Name,
			FilePath:   s.FilePath,
			Language:   s.Language,
			StartLine:  s.StartLine,
			EndLine:    s.EndLine,
			Detector:   "basic_http_name",
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Framework-aware mode
// ---------------------------------------------------------------------------

func classifyFrameworkAware(s Symbol, f File) *EntryPoint {
	// Grails — the file carries a role tag set by the indexer
	// (internal/indexer/grails.go). Any function/method in a
	// Grails controller file is a controller action.
	if f.GrailsRole == "grails_controller" {
		if s.Kind == "function" || s.Kind == "method" {
			return &EntryPoint{
				Kind:       KindGrailsAction,
				SymbolID:   s.ID,
				SymbolName: s.Name,
				FilePath:   s.FilePath,
				Language:   s.Language,
				StartLine:  s.StartLine,
				EndLine:    s.EndLine,
				Detector:   "grails_controller",
			}
		}
	}

	// Python FastAPI / Flask — decorator hint shows up in the
	// symbol's Signature when the indexer captures the surrounding
	// decorator line. Pattern: @app.get("/path"), @router.post(…),
	// @blueprint.route(…). We look for the matching substring.
	if s.Language == "python" && (s.Kind == "function" || s.Kind == "method") {
		if route, ok := pythonRouteFromSignature(s.Signature); ok {
			return &EntryPoint{
				Kind:       KindHTTPRoute,
				SymbolID:   s.ID,
				SymbolName: s.Name,
				FilePath:   s.FilePath,
				Language:   s.Language,
				StartLine:  s.StartLine,
				EndLine:    s.EndLine,
				Detector:   "fastapi_or_flask",
				Route:      route,
			}
		}
	}

	// Go chi/Gin — route registration happens at call sites, not
	// symbol declarations, so at the symbol layer we can only detect
	// "looks like an HTTP handler" via the function signature
	// containing http.ResponseWriter / gin.Context / chi.Router.
	// Anything caught here is already caught by basic_http_name if
	// the name follows convention; this branch catches handlers
	// with generic names like ServeHTTP or HandleGET.
	if s.Language == "go" && (s.Kind == "function" || s.Kind == "method") {
		if containsAny(s.Signature, "http.ResponseWriter", "gin.Context", "chi.Router", "echo.Context") {
			return &EntryPoint{
				Kind:       KindHTTPRoute,
				SymbolID:   s.ID,
				SymbolName: s.Name,
				FilePath:   s.FilePath,
				Language:   s.Language,
				StartLine:  s.StartLine,
				EndLine:    s.EndLine,
				Detector:   "go_http_signature",
			}
		}
	}

	// Next.js/Express — any file under pages/api/ or app/api/ whose
	// default-exported function is an HTTP handler. The file-path
	// convention is the dominant signal.
	if s.Language == "typescript" || s.Language == "javascript" {
		if isNextAPIHandler(s.FilePath, s.Name) {
			return &EntryPoint{
				Kind:       KindHTTPRoute,
				SymbolID:   s.ID,
				SymbolName: s.Name,
				FilePath:   s.FilePath,
				Language:   s.Language,
				StartLine:  s.StartLine,
				EndLine:    s.EndLine,
				Detector:   "nextjs_api_route",
			}
		}
	}

	return nil
}

// reFastAPIRoute matches @app.get("/path") / @router.post(...) /
// @blueprint.route("/path"). Captures the quoted path.
var reFastAPIRoute = regexp.MustCompile(`@[A-Za-z_][A-Za-z0-9_]*\.(get|post|put|delete|patch|route)\(\s*['"]([^'"]+)['"]`)

func pythonRouteFromSignature(sig string) (string, bool) {
	if sig == "" {
		return "", false
	}
	m := reFastAPIRoute.FindStringSubmatch(sig)
	if m == nil {
		return "", false
	}
	return m[2], true
}

// isNextAPIHandler returns true for TS/JS files under Next.js API
// route conventions (pages/api/ or app/api/). The symbol name is
// expected to be the default export or one of the HTTP-verb-named
// exports (GET, POST, etc.).
func isNextAPIHandler(filePath, symbolName string) bool {
	fp := filepath.ToSlash(filePath)
	// Accept both repo-relative ("pages/api/…") and absolute
	// ("/pages/api/…") forms.
	isAPI := strings.HasPrefix(fp, "pages/api/") ||
		strings.HasPrefix(fp, "app/api/") ||
		strings.Contains(fp, "/pages/api/") ||
		strings.Contains(fp, "/app/api/")
	if !isAPI {
		return false
	}
	switch symbolName {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "default", "handler":
		return true
	}
	return false
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
