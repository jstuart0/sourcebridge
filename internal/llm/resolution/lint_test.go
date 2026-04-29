// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"io/fs"
	"os"
)

// protectedWorkerMethods is the set of *worker.Client method names that
// must only be invoked through internal/worker/llmcall.Caller. Any
// invocation of one of these names on a *worker.Client receiver outside
// the allowlist is a forbidden bypass and fails this lint.
//
// Adding a new LLM-bearing RPC:
//  1. Add the method name here.
//  2. Add it to llmcall.WorkerLLM.
//  3. Add a wrapper on *llmcall.Caller.
//  4. Add an op constant in ops.go and register it in KnownOps.
var protectedWorkerMethods = map[string]struct{}{
	"AnswerQuestion":              {},
	"AnswerQuestionStream":        {},
	"AnswerQuestionWithTools":     {},
	"ClassifyQuestion":            {},
	"DecomposeQuestion":           {},
	"SynthesizeDecomposedAnswer":  {},
	"GetProviderCapabilities":     {},
	"AnalyzeSymbol":               {},
	"ReviewFile":                  {},
	"GenerateCliffNotes":          {},
	"GenerateLearningPath":        {},
	"GenerateArchitectureDiagram": {},
	"GenerateWorkflowStory":       {},
	"GenerateCodeTour":            {},
	"ExplainSystem":               {},
	"EnrichRequirement":           {},
	"ExtractSpecs":                {},
	"GenerateReport":              {},
}

// allowedFileSuffixes lists files (relative to repo root) that are
// permitted to call protected worker methods directly. The llmcall
// package's wrappers and the worker.Client's own definitions are
// allowlisted; everything else must go through Caller.
//
// Test files are excluded from the lint entirely (a fake or mock can
// implement anything it likes).
var allowedFilePathSuffixes = []string{
	"internal/worker/client.go",
	"internal/worker/llmcall/llmcall.go",
}

// allowedReceiverNames lists struct types that are *not* *worker.Client
// but happen to share a method name with one of the protected RPCs.
// Calls to these are not bypasses (e.g. qa.WorkerAgentSynthesizer has
// its own AnswerQuestionWithTools which itself goes through Caller).
//
// We allowlist by *type name* so the lint stays narrow: only direct
// calls on *worker.Client (or values whose names match the
// "worker"/"client"/"wc" pattern that the codebase consistently uses
// for the gRPC client) trip the lint. Calls on other receiver names
// are not flagged.
//
// The actual lint logic uses the inverse: it ONLY flags calls when the
// receiver expression looks like a *worker.Client (see
// looksLikeWorkerClient).

func TestNoDirectWorkerLLMCallsOutsideLLMCall(t *testing.T) {
	// Slice 1 introduces this lint and the llmcall package; slice 2
	// migrates every remaining call site. While slice 2 is in flight
	// the lint runs in REPORT-ONLY mode so the resolver/adapter
	// commit doesn't break CI. The flip to ENFORCE happens in the
	// final slice-2 commit by removing this skip.
	const enforceMode = false

	root := repoRoot(t)
	internalDir := filepath.Join(root, "internal")

	var violations []string

	walkErr := filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendor and generated dirs.
			name := d.Name()
			if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files entirely.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		// Allowlist by file path suffix.
		for _, allow := range allowedFilePathSuffixes {
			if strings.HasSuffix(filepath.ToSlash(rel), allow) {
				return nil
			}
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			methodName := sel.Sel.Name
			if _, protected := protectedWorkerMethods[methodName]; !protected {
				return true
			}
			// Allow calls when there's an escape-hatch comment on the
			// preceding line: // llmcall:allow
			pos := fset.Position(call.Pos())
			if hasAllowComment(file, fset, pos.Line) {
				return true
			}

			// Heuristic receiver-name match: flag only when the receiver
			// expression is conventionally named for a worker client.
			// This tolerates legitimate same-name methods on other
			// types (e.g. qa.WorkerAgentSynthesizer.AnswerQuestionWithTools)
			// because their receiver identifier is something else.
			if !looksLikeWorkerClient(sel.X) {
				return true
			}

			violations = append(violations, formatViolation(rel, pos.Line, methodName))
			return true
		})

		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}

	if len(violations) > 0 {
		msg := "AST lint: " + intToStr(len(violations)) + " direct *worker.Client LLM-RPC call(s) found outside the allowlist.\nEvery call must go through internal/worker/llmcall.Caller. To allow a specific exception, add a // llmcall:allow comment on the preceding line.\n\n" + strings.Join(violations, "\n")
		if enforceMode {
			t.Errorf("%s", msg)
		} else {
			t.Logf("[REPORT-ONLY] %s", msg)
		}
	}
}

// repoRoot finds the repository root by walking up from the test's CWD
// until it finds a go.mod. We resolve the path so the test is robust to
// being run from a sub-directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod in any parent of %s", cwd)
		}
		dir = parent
	}
}

// looksLikeWorkerClient returns true when expr is the kind of receiver
// expression we expect for a *worker.Client. We don't have full type
// information without a full type-check pass (which would slow this lint
// considerably), so we use the codebase's established conventions:
//
//	r.Worker.<Method>           — graphql resolver pattern
//	s.worker.<Method>           — REST server pattern
//	c.worker.<Method>           — generic field pattern
//	wc.<Method>, w.client.<Method>, h.worker.<Method>, etc.
//
// The match is conservative: any selector whose final identifier is
// "Worker" / "worker" / "client" / "wc" with a worker.Client-shaped
// owner trips the lint. This catches every call site we care about
// without false positives on unrelated types.
func looksLikeWorkerClient(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		switch e.Name {
		case "wc", "worker", "Worker", "client":
			return true
		}
	case *ast.SelectorExpr:
		switch e.Sel.Name {
		case "Worker", "worker", "client":
			return true
		}
	}
	return false
}

// hasAllowComment returns true when the line above `line` contains an
// `// llmcall:allow` comment. Lets a legitimate exception bypass the lint
// without disabling it project-wide.
func hasAllowComment(file *ast.File, fset *token.FileSet, line int) bool {
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			pos := fset.Position(c.Pos())
			if pos.Line == line-1 || pos.Line == line {
				if strings.Contains(c.Text, "llmcall:allow") {
					return true
				}
			}
		}
	}
	return false
}

func formatViolation(rel string, line int, method string) string {
	return "  " + rel + ":" + intToStr(line) + ": direct call to *worker.Client." + method + " — must go through llmcall.Caller"
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
