// Package appdeps holds the shared dependency registry for the SourceBridge
// application layer. Both rest.Server and graphql.Resolver carry an *AppDeps
// pointer; rest.Server's NewServer constructs the AppDeps once and the
// resolver-construction code reads from it via syncResolverDepsFromAppDeps.
//
// Why a separate package: appdeps is imported by both internal/api/graphql
// and internal/api/rest. Putting AppDeps in either of those packages would
// create a circular dependency.
//
// Why pointer fields: the dependencies are concrete handles (stores, clients)
// that callers already pass by pointer.
//
// ClusteringHook is intentionally absent from AppDeps: it is a closure
// constructed at wiring time from the server's clusterRunner and does not
// belong in the long-lived dependency registry. The resolver constructor
// assigns it explicitly after the sync call.
package appdeps

import (
	"context"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	gitres "github.com/sourcebridge/sourcebridge/internal/git/resolution"
	"github.com/sourcebridge/sourcebridge/internal/health"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/governance"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/search"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/sourcebridge/sourcebridge/internal/trash"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// LLMProfileLookup is the narrow interface for resolving a profile ID to its
// name and existence. Defined here (rather than in internal/api/graphql) so
// that appdeps can hold it without creating a circular import. The graphql
// package's local LLMProfileLookup interface has an identical method set and
// is therefore satisfied by any concrete type that also satisfies this one.
type LLMProfileLookup interface {
	LookupProfileName(ctx context.Context, profileID string) (name string, exists bool, err error)
}

// DrainAdmitter gates Living Wiki mutation admissions during a graceful server
// drain. Defined here for the same reason as LLMProfileLookup — to avoid a
// circular import with internal/api/graphql. The graphql package's local
// DrainAdmitter interface has an identical method set.
type DrainAdmitter interface {
	IsDraining() bool
	TryAdmitOnDemand() (token interface{ Release() }, admitted bool)
}

// AppDeps is the canonical registry of shared application-layer dependencies.
// Both rest.Server and graphql.Resolver hold a pointer to a single AppDeps
// value constructed once in NewServer. Adding a new subsystem requires:
//
//  1. A new field here.
//  2. The matching field on graphql.Resolver (exported) and/or rest.Server
//     (lowercase private), as appropriate.
//  3. One new line in the relevant sync helper
//     (syncResolverDepsFromAppDeps / syncServerDepsFromAppDeps).
type AppDeps struct {
	KnowledgeStore             knowledge.KnowledgeStore
	Worker                     *worker.Client
	LLMCaller                  *llmcall.Caller
	LLMResolver                resolution.Resolver
	Orchestrator               *orchestrator.Orchestrator
	Config                     *config.Config
	EventBus                   *events.Bus
	Flags                      featureflags.Flags
	GitResolver                gitres.Resolver
	ComprehensionStore         comprehension.Store
	HealthChecker              *health.Checker
	TrashStore                 trash.Store
	SearchSvc                  *search.Service
	ReqBooster                 *search.RequirementBooster
	QA                         *qa.Orchestrator
	LLMProfileLookup           LLMProfileLookup
	LivingWikiStore            livingwiki.Store
	LivingWikiResolver         *livingwiki.Resolver
	LivingWikiRepoStore        livingwiki.RepoSettingsStore
	LivingWikiJobResultStore   livingwiki.JobResultStore
	LivingWikiLiveOrchestrator *lworch.Orchestrator
	LivingWikiPagePublishStore livingwiki.PagePublishStatusStore
	LivingWikiAuditLog         governance.AuditLog
	ClusterStore               clustering.ClusterStore
	WorkerVersion              func(ctx context.Context) string
	DrainAdmitter              DrainAdmitter
	// EncryptionKeySet is true when the API has a resolved encryption key
	// (from SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE or the literal env var).
	// It is populated at wiring time from llmCipher.HasKey() and surfaced on
	// GET /api/v1/admin/llm-profiles so the web UI can show the correct
	// onboarding state (r1 Phase 2d).
	EncryptionKeySet bool

	// UpstreamCapacityProvider is the production adapter that reports the
	// LLM backend's declared parallel inference capacity. Populated at
	// wiring time from the global LazyAgentSynth (Phase 2). When nil
	// (e.g., in tests that don't wire a worker), the coldstart runner
	// injects no UpstreamCapacityProvider and MaxConcurrency is used as-is.
	UpstreamCapacityProvider lworch.UpstreamCapacityProvider
}
