// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestLLMBackedEnqueueIncludesProvider walks every Go file under
// internal/, cli/, and cmd/ that is NOT a *_test.go file. It flags any
// `llm.EnqueueRequest{...}` literal that:
//   - declares Subsystem matching one of the LLM-backed patterns:
//     SubsystemKnowledge / SubsystemReasoning / SubsystemRequirements /
//     SubsystemLinking / SubsystemContracts / SubsystemQA, OR
//     Subsystem("living_wiki") (string literal — not in the enum), OR
//     SubsystemClustering when JobType is the literal "relabel_clusters"
//   - AND does NOT include the LLMProvider field
//
// Subsystems that are CPU-bound (clustering with any other JobType)
// are exempt. R3 slice 3.
//
// Builders that are too dynamic for AST detection (e.g.
// `req := buildReq(...); req.LLMProvider = ...`) can be exempted via
// a `// nolint:llmprovider` comment on the EnqueueRequest line.
func TestLLMBackedEnqueueIncludesProvider(t *testing.T) {
	repoRoot := findRepoRoot(t)
	scanDirs := []string{"internal", "cli", "cmd"}

	type violation struct {
		file string
		line int
	}
	var violations []violation

	for _, scanDir := range scanDirs {
		root := filepath.Join(repoRoot, scanDir)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(repoRoot, path)
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.AllErrors)
			if perr != nil {
				return nil
			}

			// Map line → comment text (for nolint detection).
			nolintLines := map[int]bool{}
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if strings.Contains(c.Text, "nolint:llmprovider") {
						nolintLines[fset.Position(c.Pos()).Line] = true
					}
				}
			}

			ast.Inspect(f, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if !isLLMEnqueueRequestType(cl.Type) {
					return true
				}
				if !literalIsLLMBacked(cl) {
					return true
				}
				if literalHasField(cl, "LLMProvider") {
					return true
				}
				pos := fset.Position(cl.Pos())
				// Allow nolint on the literal's line OR the line above.
				if nolintLines[pos.Line] || nolintLines[pos.Line-1] {
					return true
				}
				violations = append(violations, violation{file: rel, line: pos.Line})
				return true
			})
			return nil
		})
	}

	if len(violations) > 0 {
		var b strings.Builder
		b.WriteString("LLM-backed llm.EnqueueRequest literals must include LLMProvider (R3 slice 3).\n")
		b.WriteString("Add LLMProvider: r.resolveLLMProviderForOp(ctx, repoID, op) or use a // nolint:llmprovider comment with a justification.\n\n")
		for _, v := range violations {
			b.WriteString(v.file)
			b.WriteString(":")
			intToBuilder(&b, v.line)
			b.WriteString("\n")
		}
		t.Fatal(b.String())
	}
}

// isLLMEnqueueRequestType reports whether the composite-literal type is
// llm.EnqueueRequest (with or without the package qualifier).
func isLLMEnqueueRequestType(t ast.Expr) bool {
	switch e := t.(type) {
	case *ast.SelectorExpr:
		// pkg.Identifier — match "llm.EnqueueRequest".
		if id, ok := e.X.(*ast.Ident); ok && id.Name == "llm" && e.Sel.Name == "EnqueueRequest" {
			return true
		}
	case *ast.Ident:
		// Bare "EnqueueRequest" (used inside the llm package itself —
		// which we skip since the package only contains tests for it).
		if e.Name == "EnqueueRequest" {
			return true
		}
	}
	return false
}

// literalIsLLMBacked reports whether the literal's Subsystem field
// matches one of the LLM-backed patterns. Returns true for:
//   - Subsystem: llm.SubsystemKnowledge|...|SubsystemQA (any of)
//   - Subsystem: "living_wiki" or llm.Subsystem("living_wiki")
//   - Subsystem: llm.SubsystemClustering AND JobType: "relabel_clusters"
func literalIsLLMBacked(cl *ast.CompositeLit) bool {
	subsysVal, hasSubsys := fieldValue(cl, "Subsystem")
	if !hasSubsys {
		return false
	}

	// Living-wiki is a string literal subsystem (the orchestrator accepts
	// any string). Detect both the bare string and the conversion form.
	if isLivingWikiStringLiteral(subsysVal) {
		return true
	}

	// Standard SubsystemX selector forms.
	if isOneOfSelectors(subsysVal, "SubsystemKnowledge", "SubsystemReasoning",
		"SubsystemRequirements", "SubsystemLinking", "SubsystemContracts",
		"SubsystemQA") {
		return true
	}

	// Clustering: only relabel_clusters job_type is LLM-backed.
	if isOneOfSelectors(subsysVal, "SubsystemClustering") {
		jobTypeVal, hasJobType := fieldValue(cl, "JobType")
		if hasJobType && isStringLiteral(jobTypeVal, "relabel_clusters") {
			return true
		}
	}

	return false
}

// fieldValue extracts the expression assigned to a named field of a
// composite literal (e.g. Subsystem: X).
func fieldValue(cl *ast.CompositeLit, name string) (ast.Expr, bool) {
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		if key.Name == name {
			return kv.Value, true
		}
	}
	return nil, false
}

func literalHasField(cl *ast.CompositeLit, name string) bool {
	_, ok := fieldValue(cl, name)
	return ok
}

func isOneOfSelectors(e ast.Expr, names ...string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	for _, n := range names {
		if sel.Sel.Name == n {
			return true
		}
	}
	return false
}

func isStringLiteral(e ast.Expr, want string) bool {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return false
	}
	// bl.Value includes the surrounding quotes.
	if len(bl.Value) >= 2 && bl.Value[0] == '"' && bl.Value[len(bl.Value)-1] == '"' {
		return bl.Value[1:len(bl.Value)-1] == want
	}
	return false
}

// isLivingWikiStringLiteral matches:
//   "living_wiki"            — bare string (won't compile against
//                                Subsystem typed field, but we still
//                                pattern-match for completeness)
//   llm.Subsystem("living_wiki") — type conversion form
func isLivingWikiStringLiteral(e ast.Expr) bool {
	if isStringLiteral(e, "living_wiki") {
		return true
	}
	// llm.Subsystem("living_wiki") form.
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Subsystem" {
		return false
	}
	return isStringLiteral(call.Args[0], "living_wiki")
}

func intToBuilder(b *strings.Builder, n int) {
	if n == 0 {
		b.WriteByte('0')
		return
	}
	if n < 0 {
		b.WriteByte('-')
		n = -n
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	b.Write(digits[i:])
}

// findRepoRoot walks up to find go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs cwd: %v", err)
	}
	for {
		matches, _ := filepath.Glob(filepath.Join(dir, "go.mod"))
		if len(matches) > 0 {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod from %s", dir)
		}
		dir = parent
	}
}
