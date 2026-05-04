package graphql

import "github.com/sourcebridge/sourcebridge/internal/appdeps"

// SyncResolverDepsFromAppDeps copies every shared dependency from deps into
// the corresponding exported field on r. It is called once in rest.NewServer
// after AppDeps is fully constructed and after the Resolver composite literal
// is built. The sync is idempotent — calling it again with the same AppDeps
// is a no-op from a semantic standpoint.
//
// Exported so rest.NewServer (a different package) can call it after building
// the Resolver composite literal.
//
// Per codex H-1 (Phase 2 Slice 5): the exported fields on Resolver are kept
// as-is so that composite-literal construction (Resolver{KnowledgeStore: store})
// continues to compile in tests and production. This function is the bridge
// between the canonical AppDeps registry and those explicit fields.
//
// Adding a new subsystem: add the field to appdeps.AppDeps, add the matching
// exported field to Resolver, then add one assignment line here.
func SyncResolverDepsFromAppDeps(r *Resolver, deps *appdeps.AppDeps) {
	if r == nil || deps == nil {
		return
	}
	r.KnowledgeStore = deps.KnowledgeStore
	r.Worker = deps.Worker
	r.LLMCaller = deps.LLMCaller
	r.LLMResolver = deps.LLMResolver
	r.Orchestrator = deps.Orchestrator
	r.Config = deps.Config
	r.EventBus = deps.EventBus
	r.Flags = deps.Flags
	r.GitResolver = deps.GitResolver
	r.ComprehensionStore = deps.ComprehensionStore
	r.HealthChecker = deps.HealthChecker
	r.TrashStore = deps.TrashStore
	r.SearchSvc = deps.SearchSvc
	r.ReqBooster = deps.ReqBooster
	r.QA = deps.QA
	// LLMProfileLookup: appdeps.LLMProfileLookup and graphql.LLMProfileLookup
	// have identical method sets; any concrete type satisfying one satisfies the
	// other by Go's structural interface rules.
	r.LLMProfileLookup = deps.LLMProfileLookup
	r.LivingWikiStore = deps.LivingWikiStore
	r.LivingWikiResolver = deps.LivingWikiResolver
	r.LivingWikiRepoStore = deps.LivingWikiRepoStore
	r.LivingWikiJobResultStore = deps.LivingWikiJobResultStore
	r.LivingWikiLiveOrchestrator = deps.LivingWikiLiveOrchestrator
	r.LivingWikiPagePublishStore = deps.LivingWikiPagePublishStore
	r.LivingWikiAuditLog = deps.LivingWikiAuditLog
	r.ClusterStore = deps.ClusterStore
	r.WorkerVersion = deps.WorkerVersion
	// DrainAdmitter: appdeps.DrainAdmitter and graphql.DrainAdmitter have
	// identical method sets; structural interface compatibility applies.
	r.DrainAdmitter = deps.DrainAdmitter
}
