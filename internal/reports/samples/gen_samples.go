//go:build ignore

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/activitylog"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/adr"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/glossary"
)

type fakeSymbolGraph struct{}

func (f *fakeSymbolGraph) ExportedSymbols(_ string) ([]templates.Symbol, error) {
	return []templates.Symbol{
		{
			Package: "internal/auth", Name: "Init",
			Signature:  "func Init(cfg Config) error",
			DocComment: "Init initialises the authentication subsystem. Must be called once before any handler is registered. Returns an error if the configuration is invalid or the token store is unreachable.",
			FilePath: "internal/auth/auth.go", StartLine: 22, EndLine: 41,
		},
		{
			Package: "internal/auth", Name: "Middleware",
			Signature:  "func Middleware(next http.Handler) http.Handler",
			DocComment: "Middleware wraps next and enforces authentication on every request. Unauthenticated requests receive a 401 response. Panics if Init has not been called.",
			FilePath: "internal/auth/middleware.go", StartLine: 10, EndLine: 35,
		},
		{
			Package: "internal/auth", Name: "RequireRole",
			Signature:  "func RequireRole(role string) func(http.Handler) http.Handler",
			DocComment: "RequireRole returns a middleware that enforces that the authenticated user holds role. Returns 403 when the user lacks the role. Panics if Init has not been called.",
			FilePath: "internal/auth/role.go", StartLine: 18, EndLine: 44,
		},
		{
			Package: "internal/jobs", Name: "Dispatcher",
			Signature:  "type Dispatcher struct",
			DocComment: "Dispatcher manages the bounded worker pool. MaxConcurrency (default 8) limits simultaneous job execution. Callers enqueue via Submit; the call blocks when the pool is full.",
			FilePath: "internal/jobs/dispatcher.go", StartLine: 12, EndLine: 28,
		},
		{
			Package: "internal/jobs", Name: "Submit",
			Signature:  "func (d *Dispatcher) Submit(ctx context.Context, job Job) error",
			DocComment: "Submit enqueues job for execution. Blocks until a worker slot is available or ctx is cancelled. Returns ctx.Err() on cancellation; never drops a job silently.",
			FilePath: "internal/jobs/dispatcher.go", StartLine: 44, EndLine: 62,
		},
		{
			Package: "internal/qa", Name: "Run",
			Signature:  "func Run(ctx context.Context, req Request) (Result, error)",
			DocComment: "Run executes an agentic QA session against the indexed codebase. The session is bounded by req.Budget; results include citations in the (path:start-end) format.",
			FilePath: "internal/qa/qa.go", StartLine: 31, EndLine: 78,
		},
	}, nil
}

type fakeGitLog struct{}

func (f *fakeGitLog) Commits(_ string) ([]templates.Commit, error) {
	base := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	return []templates.Commit{
		{
			SHA: "a1b2c3d4e5f60001", ShortSHA: "a1b2c3d",
			Author: "alice", AuthorEmail: "alice@example.com",
			Subject:   "decision: replace legacy-jwt with golang-jwt v5",
			Body:      "The legacy-jwt v2 library has 2 open CVEs (CVE-2024-1234, CVE-2024-5678) and has not been updated since 2022.\n\nWe evaluated golang-jwt/jwt v5, lestrrat-go/jwx v2, and a hand-rolled HMAC approach. golang-jwt v5 is the best fit: actively maintained, JWK rotation supported natively, zero new dependencies.\n\nMigration: token format changed from RS256 to ES256. Existing sessions invalidated on first deploy.",
			Timestamp: base, FilesChanged: 12, Insertions: 140, Deletions: 200,
			TouchedPaths: []string{"internal/auth/jwt.go", "internal/auth/middleware.go", "internal/auth/token.go", "internal/auth/legacy.go"},
		},
		{
			SHA: "b2c3d4e5f6001111", ShortSHA: "b2c3d4e",
			Author: "bob", AuthorEmail: "bob@example.com",
			Subject:   "feat: add RequireRole middleware",
			Timestamp: base.Add(2 * time.Hour), FilesChanged: 3, Insertions: 65,
			TouchedPaths: []string{"internal/auth/role.go", "internal/auth/role_test.go", "internal/api/rest.go"},
		},
		{
			SHA: "c3d4e5f600222222", ShortSHA: "c3d4e5f",
			Author: "alice", AuthorEmail: "alice@example.com",
			Subject:   "fix: RequireRole panic on missing token claim",
			Timestamp: base.Add(4 * time.Hour), FilesChanged: 2, Insertions: 14, Deletions: 3,
			TouchedPaths: []string{"internal/auth/role.go", "internal/auth/role_test.go"},
		},
		{
			SHA: "d4e5f60033333333", ShortSHA: "d4e5f60",
			Author: "carol", AuthorEmail: "carol@example.com",
			Subject:   "chore: update dependency graph after auth refactor",
			Timestamp: base.Add(-7*24*time.Hour + time.Hour), FilesChanged: 1, Insertions: 5, Deletions: 5,
			TouchedPaths: []string{"go.sum"},
		},
		{
			SHA: "e5f6007444444444", ShortSHA: "e5f6007",
			Author: "bob", AuthorEmail: "bob@example.com",
			Subject:   "feat: add job dispatcher to internal/jobs",
			Body:      "BREAKING CHANGE: the old internal/worker package is removed. Callers must migrate to internal/jobs.Dispatcher.",
			Timestamp: base.Add(-7*24*time.Hour + 2*time.Hour), FilesChanged: 8, Insertions: 210,
			TouchedPaths: []string{"internal/jobs/dispatcher.go", "internal/jobs/worker.go", "internal/jobs/dispatcher_test.go"},
		},
		{
			SHA: "f60012355555555", ShortSHA: "f600123",
			Author: "carol", AuthorEmail: "carol@example.com",
			Subject:   "adr: adopt event-driven architecture for order processing",
			Body:      "Context: the synchronous RPC chain between the order, payment, and fulfilment services was causing cascading timeouts under load.\n\nDecision: we are introducing an event bus (Kafka) between these three services. Orders emit OrderPlaced events; payment and fulfilment subscribe independently.\n\nConsequences: eventual consistency for order status. We accept the complexity of idempotent consumers in exchange for fault isolation.",
			Timestamp: base.Add(-14*24*time.Hour + time.Hour), FilesChanged: 0,
		},
	}, nil
}

type fakeLLM struct{}

func (f *fakeLLM) Complete(_ context.Context, system, user string) (string, error) {
	// Distinguish ADR from digest by checking the system prompt.
	isADR := strings.Contains(system, "Architectural Decision Record") || strings.Contains(system, "## Context")
	if !isADR && strings.Contains(system, "weekly engineering digest") {
		// Activity log digest — return concise digest prose.
		if strings.Contains(user, "Week of 20 April") {
			return `This week the team shipped the RequireRole middleware for route-level access control (internal/auth/role.go:18-44), patched a nil-pointer panic in RequireRole when the JWT claim was absent, and merged the JWT library migration to golang-jwt v5. 3 commits, 17 files changed, net -49 lines.

The JWT library migration was the most impactful change: legacy-jwt v2 is fully removed, token format changed from RS256 to ES256. Existing sessions invalidated on first deploy. The RequireRole middleware is covered by tests; the panic fix adds 2 regression tests to internal/auth/role_test.go.`, nil
		}
		if strings.Contains(user, "Week of 13 April") {
			return `This week the team landed the initial internal/jobs.Dispatcher package — a bounded worker pool replacing the old internal/worker package. 2 commits, 9 files changed, net +215 lines.

BREAKING CHANGE: internal/worker is removed. Callers must migrate to internal/jobs.Dispatcher. The new package exposes Submit (blocking until a slot is available) and enforces MaxConcurrency at 8 by default. A go.sum update from carol reflects updated indirect dependencies.`, nil
		}
		if strings.Contains(user, "Week of 6 April") {
			return `This week carol committed the event-driven architecture decision record for order processing. 1 commit, 0 files changed (documentation-only).

No code changes landed this week. The ADR documents the shift to Kafka-backed order processing and is referenced by the order service implementation work tracked separately.`, nil
		}
		return "No digest available for this week.", nil
	}
	if strings.Contains(user, "legacy-jwt") || strings.Contains(user, "golang-jwt") {
		return `## Context
The authentication library in use (legacy-jwt v2) had not received a security patch in over 3 years and carried 2 open CVEs (CVE-2024-1234, CVE-2024-5678). The team evaluated 3 replacement libraries against criteria of active maintenance, JWK rotation support, and minimal new dependencies.

## Decision
We replaced legacy-jwt v2 with golang-jwt/jwt v5 (internal/auth/jwt.go:1-45). golang-jwt v5 is actively maintained, supports native JWK rotation, and introduces zero new transitive dependencies. The token algorithm was changed from RS256 to ES256 to align with the current key infrastructure.

## Consequences
All token validation paths now route through golang-jwt v5. The token format change invalidates existing sessions on first deployment — a one-time disruption that was accepted given the security posture improvement. The legacy parse path (internal/auth/legacy.go) is removed. Sessions older than the deploy window must re-authenticate.`, nil
	}
	if strings.Contains(user, "event-driven") || strings.Contains(user, "Kafka") || strings.Contains(user, "order processing") {
		return `## Context
The synchronous RPC chain between the order, payment, and fulfilment services was causing cascading timeouts under sustained load (>500 rps). A single slow payment provider call was blocking order acknowledgement for 30+ seconds.

## Decision
We are introducing Kafka as an event bus between these three services. Orders emit an OrderPlaced event; payment and fulfilment subscribe independently (internal/events/order.go). This breaks the synchronous dependency chain and allows each service to scale independently.

## Consequences
Order status updates are now eventually consistent. We accept this trade-off in exchange for fault isolation — a payment service outage no longer stalls order acknowledgement. Consumers must be idempotent; this is enforced via the deduplication key on the Kafka consumer group.`, nil
	}
	// Weekly digest prompt
	return `This week the team shipped the RequireRole middleware for route-level access control (internal/auth/role.go:18-44) and patched a nil-pointer panic that occurred when the JWT claim was absent. 3 commits, 7 files changed, net +76 lines.

No breaking changes this week. The panic fix was a 14-line targeted change covered by 2 new regression tests in internal/auth/role_test.go. The RequireRole middleware follows the same pattern as Middleware (internal/auth/middleware.go:10-35) and composes correctly with it.`, nil
}

func main() {
	ctx := context.Background()
	fixedTime := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	outDir := "samples"

	// Find repo root by walking up from this file's compiled path.
	// When run with `go run`, cwd is the repo root already if invoked correctly.
	// We write to samples/ relative to cwd.

	// --- Glossary ---
	{
		g := glossary.New()
		page, err := g.Generate(ctx, templates.GenerateInput{
			RepoID: "sourcebridge", Audience: quality.AudienceEngineers,
			SymbolGraph: &fakeSymbolGraph{}, Now: fixedTime,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "glossary: %v\n", err)
			os.Exit(1)
		}
		var buf bytes.Buffer
		if err := markdown.Write(&buf, page); err != nil {
			fmt.Fprintf(os.Stderr, "glossary write: %v\n", err)
			os.Exit(1)
		}
		writeFile(outDir+"/glossary-example.md", buf.Bytes())
	}

	// --- Activity log ---
	{
		al := activitylog.New()
		page, err := al.Generate(ctx, templates.GenerateInput{
			RepoID: "sourcebridge", Audience: quality.AudienceEngineers,
			GitLog: &fakeGitLog{}, LLM: &fakeLLM{}, Now: fixedTime,
			Config: templates.TemplateConfig{EnableLLMDigest: true},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "activity_log: %v\n", err)
			os.Exit(1)
		}
		var buf bytes.Buffer
		if err := markdown.Write(&buf, page); err != nil {
			fmt.Fprintf(os.Stderr, "activity_log write: %v\n", err)
			os.Exit(1)
		}
		writeFile(outDir+"/activity-log-example.md", buf.Bytes())
	}

	// --- ADRs ---
	{
		result, err := adr.GenerateAll(ctx, templates.GenerateInput{
			RepoID: "sourcebridge", Audience: quality.AudienceEngineers,
			GitLog: &fakeGitLog{}, LLM: &fakeLLM{}, Now: fixedTime,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "adr: %v\n", err)
			os.Exit(1)
		}
		var buf bytes.Buffer
		for i, page := range result.Pages {
			if i > 0 {
				buf.WriteString("\n---\n\n")
			}
			if err := markdown.Write(&buf, page); err != nil {
				fmt.Fprintf(os.Stderr, "adr write: %v\n", err)
				os.Exit(1)
			}
		}
		writeFile(outDir+"/adr-example.md", buf.Bytes())
		fmt.Printf("wrote %s/adr-example.md (%d ADRs)\n", outDir, len(result.Pages))
	}
}

func writeFile(path string, data []byte) {
	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", path)
}
