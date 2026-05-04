package rest

import (
	"context"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
)

// syncServerDepsFromAppDeps copies every shared dependency from deps into the
// corresponding lowercase field on s. Called once in NewServer after AppDeps
// is fully constructed. The sync is idempotent — the Server's lowercase fields
// are already set from WithXxx options before NewServer calls this function,
// so the write-back is a no-op semantically. It ensures AppDeps stays the
// single source of truth for any code that reads from it post-construction.
//
// Per codex H-1 (Phase 2 Slice 5): the existing lowercase fields on Server
// are preserved unchanged. AppDeps mirrors them. Adding a new subsystem:
//
//  1. Add the field to appdeps.AppDeps.
//  2. Add the matching lowercase field to Server (or WithXxx option).
//  3. Add one assignment line here.
func syncServerDepsFromAppDeps(s *Server, deps *appdeps.AppDeps) {
	if s == nil || deps == nil {
		return
	}
	s.knowledgeStore = deps.KnowledgeStore
	s.worker = deps.Worker
	s.llmCaller = deps.LLMCaller
	s.llmResolver = deps.LLMResolver
	s.orchestrator = deps.Orchestrator
	s.cfg = deps.Config
	s.eventBus = deps.EventBus
	s.flags = deps.Flags
	s.gitResolver = deps.GitResolver
	s.comprehensionStore = deps.ComprehensionStore
	s.healthChecker = deps.HealthChecker
	s.trashStore = deps.TrashStore
	s.searchSvc = deps.SearchSvc
	s.reqBooster = deps.ReqBooster
	s.qaOrchestrator = deps.QA
	s.livingWikiStore = deps.LivingWikiStore
	s.livingWikiResolver = deps.LivingWikiResolver
	s.livingWikiRepoStore = deps.LivingWikiRepoStore
	s.livingWikiJobResultStore = deps.LivingWikiJobResultStore
	s.livingWikiLiveOrchestrator = deps.LivingWikiLiveOrchestrator
	s.livingWikiPagePublishStore = deps.LivingWikiPagePublishStore
	// clusterStore: Server does not have a standalone clusterStore field —
	// the cluster-compatible store is extracted from s.store via type assertion
	// in NewServer. AppDeps.ClusterStore is populated from that assertion and
	// wired into the GraphQL resolver; there is no write-back needed here.

	// LivingWikiAuditLog: not stored on Server (it is an enterprise-only dep
	// threaded directly into the Resolver). AppDeps carries it so the resolver
	// sync can populate r.LivingWikiAuditLog without a separate wiring path.

	// LLMProfileLookup: wired via s.llmProfileStore (a broader interface); the
	// narrowed lookup adapter is built at setupRouter time. AppDeps.LLMProfileLookup
	// is set in NewServer's AppDeps construction block from the same adapter.

	// WorkerVersion and DrainAdmitter are closures/interfaces constructed from
	// Server methods, not from standalone fields. They are set directly on
	// AppDeps in the NewServer wiring block rather than synced here.
}

// buildWorkerVersionFunc returns the WorkerVersion closure used by both
// AppDeps and (via sync) the GraphQL resolver. Extracted here so
// NewServer remains readable.
func buildWorkerVersionFunc(s *Server) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		if s.workerVersionLookup == nil {
			return ""
		}
		return s.workerVersionLookup.get(ctx)
	}
}
