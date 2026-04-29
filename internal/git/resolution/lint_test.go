// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDirectCfgGitReads enforces that no production code outside a
// small allowlist reads `cfg.Git.DefaultToken` or `cfg.Git.SSHKeyPath`
// directly. R3 slice 2: every consumer must go through the
// gitres.Resolver so cross-replica DB saves are visible without
// restart and the legacy boot-time merge can never quietly come back.
//
// The check walks every Go file under internal/, cli/, and cmd/ that
// is NOT a *_test.go file. It flags any selector expression matching
// `<x>.Git.DefaultToken` or `<x>.Git.SSHKeyPath` where the parent of
// `Git` is named "Config" or "cfg" — i.e. it would be a read against
// *config.Config rather than against, say, gitres.Snapshot or
// db.LLMConfigRecord.
//
// The allowlist is intentionally small. A new entry should be a
// deliberate, reviewed exception (e.g. the bootstrap layer in
// cli/serve.go that captures cfg.Git BY VALUE into the resolver).
func TestNoDirectCfgGitReads(t *testing.T) {
	// Allowed file → allowed top-level function names. Any read of
	// cfg.Git.* outside these scopes fails the test.
	//
	// IMPORTANT: a future contributor adding a new entry here should
	// also add a comment in the touched file explaining why a direct
	// read is necessary.
	allowlist := map[string]map[string]bool{
		// Boot-time env-bootstrap capture into the resolver.
		"cli/serve.go": {"runServe": true},
		// Resolver internals — this package owns cfg.Git as the
		// env-bootstrap layer.
		"internal/git/resolution/resolver.go":     {"*": true},
		"internal/git/resolution/ssh_validate.go": {"*": true},
		// Test files inside this package must be allowed (they read
		// envBoot fields constructed from a config.GitConfig literal).
		"internal/git/resolution/resolver_test.go":     {"*": true},
		"internal/git/resolution/ssh_validate_test.go": {"*": true},
		"internal/git/resolution/lint_test.go":         {"*": true},
		// Embedded/test-mode fallback in the REST git_config handler.
		// Production wiring sets the resolver; the cfg.Git read only
		// fires when the resolver is nil.
		"internal/api/rest/git_config.go": {"handleGetGitConfig": true},
		// Admin snapshot fallback when no resolver is wired.
		"internal/api/rest/admin.go": {"adminGitView": true},
		// GraphQL resolver's legacy fallback (when GitResolver is nil
		// in tests). Production wiring sets GitResolver so this path
		// is dead in live deployments. resolveGitCredentialsForOp is
		// the codex r2 medium variant that takes an op label for the
		// LogResolved structured log line; both wrap the same fallback.
		"internal/api/graphql/resolver.go": {
			"resolveGitCredentials":      true,
			"resolveGitCredentialsForOp": true,
		},
	}

	repoRoot := findRepoRoot(t)
	scanDirs := []string{"internal", "cli", "cmd"}

	var violations []lintViolation

	for _, scanDir := range scanDirs {
		root := filepath.Join(repoRoot, scanDir)
		if _, err := filepath.Glob(root); err != nil {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Skip test files — the lint applies to production code.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}

			rel, _ := filepath.Rel(repoRoot, path)
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.AllErrors)
			if perr != nil {
				// Skip unparseable files; other tooling will catch them.
				return nil
			}

			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// Match selector chain ending in .Git.DefaultToken or
				// .Git.SSHKeyPath: the outer SelectorExpr.X must itself
				// be a SelectorExpr whose Sel.Name == "Git", and the
				// outer Sel.Name must be one of the protected fields.
				if sel.Sel.Name != "DefaultToken" && sel.Sel.Name != "SSHKeyPath" {
					return true
				}
				inner, ok := sel.X.(*ast.SelectorExpr)
				if !ok || inner.Sel.Name != "Git" {
					return true
				}

				// Accept only matches against a config-shaped parent.
				// We don't try to resolve types fully (would require
				// go/types + package loader); instead we accept any
				// inner.X that is an Ident or another SelectorExpr —
				// in practice that catches `cfg.Git.X`, `s.cfg.Git.X`,
				// `r.Config.Git.X`, `c.Git.X` etc.

				// Find the enclosing top-level func name.
				fnName := enclosingFuncName(f, fset, n.Pos())

				// Is this allowed?
				allowed := false
				if allowedFns, ok := allowlist[rel]; ok {
					if allowedFns["*"] || allowedFns[fnName] {
						allowed = true
					}
				}
				if !allowed {
					pos := fset.Position(n.Pos())
					violations = append(violations, lintViolation{
						file: rel,
						line: pos.Line,
						fn:   fnName,
						expr: exprString(sel),
					})
				}
				return true
			})
			return nil
		})
	}

	if len(violations) > 0 {
		t.Fatalf("forbidden direct cfg.Git.* reads found (R3 slice 2: every consumer MUST go through gitres.Resolver). Add a deliberate allowlist entry in lint_test.go if a new exception is justified.\n\n%s",
			formatViolations(violations))
	}
}

func enclosingFuncName(f *ast.File, fset *token.FileSet, pos token.Pos) string {
	var name string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Body == nil {
			continue
		}
		if fn.Body.Pos() <= pos && pos <= fn.Body.End() {
			name = fn.Name.Name
			break
		}
	}
	if name == "" {
		return "<package-init-or-var>"
	}
	return name
}

func exprString(s *ast.SelectorExpr) string {
	// Cheap pretty-printer for identifier-only selector chains.
	var inner string
	switch x := s.X.(type) {
	case *ast.Ident:
		inner = x.Name
	case *ast.SelectorExpr:
		inner = exprString(x)
	default:
		inner = "?"
	}
	return inner + "." + s.Sel.Name
}

type lintViolation struct {
	file string
	line int
	fn   string
	expr string
}

func formatViolations(vs []lintViolation) string {
	var b strings.Builder
	for _, v := range vs {
		b.WriteString(v.file)
		b.WriteString(":")
		// Hand-format line number to avoid pulling in strconv noise.
		writeInt(&b, v.line)
		b.WriteString(" (")
		b.WriteString(v.fn)
		b.WriteString("): ")
		b.WriteString(v.expr)
		b.WriteString("\n")
	}
	return b.String()
}

func writeInt(b *strings.Builder, n int) {
	if n == 0 {
		b.WriteString("0")
		return
	}
	if n < 0 {
		b.WriteString("-")
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

// findRepoRoot walks up from the test's package dir until it finds a
// go.mod. Necessary because go test sets cwd to the package dir, and
// the lint scans repo-relative paths.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs cwd: %v", err)
	}
	for {
		if _, err := filepath.Glob(filepath.Join(dir, "go.mod")); err == nil {
			matches, _ := filepath.Glob(filepath.Join(dir, "go.mod"))
			if len(matches) > 0 {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod walking up from %s", dir)
		}
		dir = parent
	}
}
