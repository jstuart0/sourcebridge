package graphql

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/entitlements"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	gitres "github.com/sourcebridge/sourcebridge/internal/git/resolution"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/health"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/governance"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/search"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/sourcebridge/sourcebridge/internal/trash"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// GitConfigLoader is the legacy adapter the graphql package used before
// R3 slice 2 introduced gitres.Resolver. It is kept ONLY to preserve
// the option name on rest.Server for backward-compatible test wiring
// (the live runtime path uses Resolver.GitResolver). When both are set,
// the resolver wins.
type GitConfigLoader interface {
	LoadGitConfig() (token, sshKeyPath string, err error)
}

// DrainAdmitter is the interface the GraphQL resolver uses to (a) check
// whether the server is draining and (b) atomically admit on-demand Living
// Wiki requests to the drain counter. CA-142.
//
// The concrete implementation is *rest.serverDrainAdmitter,
// wired via graphql.Resolver.DrainAdmitter at server construction. Nil
// means drain protection is not active (embedded mode, tests).
type DrainAdmitter interface {
	// IsDraining returns true when the server has received SIGTERM or a
	// /internal/begin-drain call and is waiting for jobs to finish. Used
	// by cold-start mutations that do not count toward the on-demand total.
	IsDraining() bool

	// TryAdmitOnDemand atomically checks whether the server is draining
	// and, if not, increments the in-flight counter. Returns (token, true)
	// when admitted, or (nil, false) when the server is draining.
	//
	// The check and increment happen under the same mutex that BeginDrain
	// uses to flip the draining flag, so there is no window between
	// passing the gate and incrementing the counter. The caller MUST call
	// token.Release() exactly once (typically via defer) when admitted.
	TryAdmitOnDemand() (token interface{ Release() }, admitted bool)
}

// LLMProfileLookup is the narrow read-side interface the GraphQL layer
// uses to (a) validate that a per-repo override's profileId references
// an existing profile at save time, and (b) resolve profileName for the
// RepositoryLLMOverride.profileName field at read time.
//
// Slice 3 of the LLM provider profiles plan introduces this. The cli
// wiring layer (cli/serve.go) implements it on top of the slice-1
// SurrealLLMProfileStore via a small adapter; tests use an in-memory
// fake. The interface is intentionally narrow: GraphQL never needs to
// see profile credentials, so we don't expose api_key, base_url, etc.
// here — only the existence/name pair.
//
// LookupProfileName returns:
//   - ("Default", true, nil) when the profile exists.
//   - ("", false, nil) when the profile is missing (deleted). This
//     drives the PROFILE_NO_LONGER_EXISTS error code on the field +
//     mutation.
//   - ("", false, err) on store failures (DB outage etc.) — caller
//     surfaces a generic error.
type LLMProfileLookup interface {
	LookupProfileName(ctx context.Context, profileID string) (name string, exists bool, err error)
}

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require here.

type Resolver struct {
	// Deps is the shared dependency registry constructed once in rest.NewServer
	// and propagated to the Resolver via syncResolverDepsFromAppDeps. New code
	// should read from Deps rather than adding standalone fields. The explicit
	// fields below are preserved for backward composite-literal construction
	// (Resolver{KnowledgeStore: store, ...}) — embedding would break them.
	// See internal/appdeps for the canonical registry.
	Deps *appdeps.AppDeps

	Store graph.GraphStore
	// KnowledgeStore is preserved as an explicit field for backward
	// composite-literal construction; new code should read r.Deps.KnowledgeStore
	// directly. Sync via syncResolverDepsFromAppDeps in resolver_deps.go.
	KnowledgeStore knowledge.KnowledgeStore // nil when knowledge persistence is unavailable
	// Worker is preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.Worker directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	Worker *worker.Client // nil when AI features are unavailable
	// LLMCaller is the LLM-aware adapter around Worker. All GraphQL
	// resolvers that perform LLM-bearing RPCs must call LLMCaller.<RPC>
	// rather than Worker.<RPC> directly so workspace-saved settings are
	// attached to the outgoing gRPC metadata. Nil when Worker is nil.
	// Preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.LLMCaller directly. Sync
	// via syncResolverDepsFromAppDeps in resolver_deps.go.
	LLMCaller *llmcall.Caller
	// LLMResolver is the runtime LLM-config resolver. Today only the
	// llmcall.Caller goes through it directly; resolvers that need to
	// inspect the resolved snapshot (e.g. for telemetry stamping) can
	// also call Resolve. Nil only in tests / embedded mode.
	// Preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.LLMResolver directly. Sync
	// via syncResolverDepsFromAppDeps in resolver_deps.go.
	LLMResolver resolution.Resolver
	// Orchestrator is preserved as an explicit field for backward
	// composite-literal construction; new code should read r.Deps.Orchestrator
	// directly. Sync via syncResolverDepsFromAppDeps in resolver_deps.go.
	Orchestrator *orchestrator.Orchestrator // nil when llm orchestration is unavailable (degraded mode)
	// Config is preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.Config directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	Config *config.Config // application configuration
	// EventBus is preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.EventBus directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	EventBus *events.Bus // in-process event bus for SSE notifications
	// Flags is preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.Flags directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	Flags featureflags.Flags // backend startup-time feature flags
	// Plan is the entitlement plan resolved at boot via BootCurrentPlan().
	// Per-request capability resolution reads this field instead of calling
	// currentPlan() (which reads env vars) on every GraphQL request.
	// Zero value normalized to PlanOSS at the resolveCapabilities call site
	// for backward composite-literal construction compatibility.
	Plan      entitlements.Plan
	GitConfig GitConfigLoader // legacy; nil-safe — when GitResolver is set it's authoritative
	// GitResolver is the runtime git-credential resolver (R3 slice 2).
	// All clone / fetch / upstream-probe call sites resolve credentials
	// through this resolver so an admin save on replica A is visible to
	// replica B on the very next op (version-cell pattern). When nil,
	// resolveGitCredentials falls back to the legacy GitConfig loader
	// (test wiring) and finally r.Config.Git (in-memory env bootstrap).
	// Preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.GitResolver directly. Sync
	// via syncResolverDepsFromAppDeps in resolver_deps.go.
	GitResolver gitres.Resolver
	// ComprehensionStore is preserved as an explicit field for backward
	// composite-literal construction; new code should read
	// r.Deps.ComprehensionStore directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	ComprehensionStore comprehension.Store // comprehension settings + model capabilities; nil when unavailable
	// HealthChecker is preserved as an explicit field for backward
	// composite-literal construction; new code should read r.Deps.HealthChecker
	// directly. Sync via syncResolverDepsFromAppDeps in resolver_deps.go.
	HealthChecker *health.Checker // shared DB+worker health probe; nil = no live checks (embedded/test mode)
	// LLMProfileLookup resolves profile id → name and "exists" for the
	// per-repo override path (slice 3). Nil when running pre-profile
	// (embedded mode); the GraphQL field/mutation degrade gracefully
	// (mutations skip the validation step; the field returns nil for
	// profileName but never PROFILE_NO_LONGER_EXISTS without proof).
	// Preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.LLMProfileLookup directly.
	// Sync via syncResolverDepsFromAppDeps in resolver_deps.go.
	LLMProfileLookup LLMProfileLookup
	// LivingWikiStore is preserved as an explicit field for backward
	// composite-literal construction; new code should read r.Deps.LivingWikiStore
	// directly. Sync via syncResolverDepsFromAppDeps in resolver_deps.go.
	LivingWikiStore livingwiki.Store // living-wiki UI settings; nil when unavailable
	// LivingWikiResolver is preserved as an explicit field for backward
	// composite-literal construction; new code should read
	// r.Deps.LivingWikiResolver directly. Sync via syncResolverDepsFromAppDeps
	// in resolver_deps.go.
	LivingWikiResolver *livingwiki.Resolver // resolved living-wiki settings (UI + env fallback)
	// LivingWikiRepoStore is preserved as an explicit field for backward
	// composite-literal construction; new code should read
	// r.Deps.LivingWikiRepoStore directly. Sync via syncResolverDepsFromAppDeps
	// in resolver_deps.go.
	LivingWikiRepoStore livingwiki.RepoSettingsStore // per-repo living-wiki opt-in; nil when unavailable
	// LivingWikiJobResultStore is preserved as an explicit field for backward
	// composite-literal construction; new code should read
	// r.Deps.LivingWikiJobResultStore directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	LivingWikiJobResultStore livingwiki.JobResultStore // per-run job result history; nil when unavailable
	// LivingWikiLiveOrchestrator is preserved as an explicit field for backward
	// composite-literal construction; new code should read
	// r.Deps.LivingWikiLiveOrchestrator directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	LivingWikiLiveOrchestrator *lworch.Orchestrator // living-wiki page-generation orchestrator; nil when feature unavailable
	// LivingWikiPagePublishStore is preserved as an explicit field for backward
	// composite-literal construction; new code should read
	// r.Deps.LivingWikiPagePublishStore directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	LivingWikiPagePublishStore livingwiki.PagePublishStatusStore // per-page dispatch state (Phase 1); nil skips fingerprint tracking
	// TrashStore is preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.TrashStore directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	TrashStore trash.Store // soft-delete recycle bin; nil when the feature is disabled or unavailable
	// QA is preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.QA directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	QA *qa.Orchestrator // server-side deep-QA orchestrator; nil when server-side QA is disabled
	// SearchSvc is preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.SearchSvc directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	SearchSvc *search.Service // hybrid retrieval backbone; nil falls back to legacy substring search
	// ReqBooster is preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.ReqBooster directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	ReqBooster *search.RequirementBooster // requirement-link cache; link mutations call Invalidate so subsequent searches see fresh links
	// LivingWikiAuditLog is preserved as an explicit field for backward
	// composite-literal construction; new code should read
	// r.Deps.LivingWikiAuditLog directly. Sync via syncResolverDepsFromAppDeps
	// in resolver_deps.go.
	LivingWikiAuditLog governance.AuditLog // audit trail for credential rotations and settings changes; nil disables audit logging
	// ClusteringHook is called after each successful index run with (repoID,
	// commitSHA) to enqueue an async clustering job. Nil = clustering disabled.
	// Not in AppDeps: this is a closure constructed at wiring time; see
	// resolver_deps.go and the appdeps package doc for rationale.
	ClusteringHook func(repoID, commitSHA string)
	// ClusterStore provides cluster lookups for Living Wiki taxonomy resolution.
	// When nil, resolveTaxonomy falls back to the package-path heuristic.
	// Preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.ClusterStore directly. Sync via
	// syncResolverDepsFromAppDeps in resolver_deps.go.
	ClusterStore clustering.ClusterStore
	// WorkerVersion returns the worker's reported version string,
	// empty when the worker is nil/unreachable/slow. Wired from the
	// REST server's cached lookup (internal/api/rest/version.go) so
	// the GraphQL Query.version field reads the same cached value
	// as REST /api/v1/version. Nil-safe: tests construct Resolver
	// without WorkerVersion and the resolver returns "" for that
	// field. CA-138.
	// Preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.WorkerVersion directly. Sync
	// via syncResolverDepsFromAppDeps in resolver_deps.go.
	WorkerVersion func(ctx context.Context) string
	// DrainAdmitter gates Living Wiki mutation admissions during a
	// graceful server drain. When the server is draining, new cold-start
	// and on-demand requests are rejected with SERVER_DRAINING. On-demand
	// requests that pass the gate are counted so AwaitDrain waits for
	// them. Nil in embedded/test mode — drain protection is inactive.
	// CA-142.
	// Preserved as an explicit field for backward composite-literal
	// construction; new code should read r.Deps.DrainAdmitter directly. Sync
	// via syncResolverDepsFromAppDeps in resolver_deps.go.
	DrainAdmitter DrainAdmitter
}

// getStore returns the per-request tenant-filtered store when available,
// falling back to the default store. In enterprise multi-tenant mode,
// RepoAccessMiddleware injects a TenantFilteredStore into the context.
func (r *Resolver) getStore(ctx context.Context) graph.GraphStore {
	if s := middleware.StoreFromContext(ctx); s != nil {
		return s
	}
	return r.Store
}

// resolveGitCredentials returns the current git token and SSH key path
// via the runtime git resolver (R3 slice 2). The resolver enforces the
// same source order as the LLM resolver: workspace DB > env-bootstrap >
// builtin. On a workspace-row decrypt failure (corruption, missing key,
// etc.) the resolver returns an empty Token and a non-nil
// Snapshot.IntegrityError; this function surfaces that as a non-nil err
// — callers MUST check it and abort their operation rather than silently
// fall back to an env-only value (fail-closed; same semantics the LLM
// path enforces for ca_llm_config).
//
// Context is threaded so a cancelled GraphQL request bypasses the DB
// version probe rather than completing on context.Background.
//
// Backward-compat: when r.GitResolver is nil (e.g. older test wiring) we
// fall back to the legacy GitConfigLoader path. The legacy path cannot
// surface IntegrityError — it only ever returns env-shadowed values —
// so production deployments MUST wire the resolver.
//
// resolveGitCredentialsForOp(ctx, "<op>") so the structured log line
// carries op context. This zero-arg form is kept for older tests that
// haven't been updated and is asserted as allowlisted in
// internal/git/resolution/lint_test.go (resolveGitCredentials).
//
//nolint:unused // Backward-compat shim: every active caller now uses
func (r *Resolver) resolveGitCredentials(ctx context.Context) (token, sshKeyPath string, err error) {
	return r.resolveGitCredentialsForOp(ctx, "graphql")
}

// resolveGitCredentialsForOp is the codex r2 medium fix: every top-level
// git op (clone, fetch, upstream probe, import) names itself so the
// resulting `git creds resolved` log line carries op context. Operators
// grep for `event="git creds resolved" sources_token=db` to verify
// workspace settings are taking effect end-to-end. Token material is
// never logged.
func (r *Resolver) resolveGitCredentialsForOp(ctx context.Context, op string) (token, sshKeyPath string, err error) {
	if r.GitResolver != nil {
		snap, resolveErr := r.GitResolver.Resolve(ctx)
		if resolveErr != nil {
			return "", "", fmt.Errorf("resolve git creds: %w", resolveErr)
		}
		if snap.IntegrityError != nil {
			// Hard fail-closed: corrupt envelope or missing key.
			return "", "", fmt.Errorf("git creds integrity failure: %w", snap.IntegrityError)
		}
		gitres.LogResolved(slog.Default(), op, snap)
		return snap.Token, snap.SSHKeyPath, nil
	}

	// Legacy fallback (test wiring).
	if r.GitConfig != nil {
		if t, s, lerr := r.GitConfig.LoadGitConfig(); lerr == nil {
			if t != "" {
				token = t
			}
			if s != "" {
				sshKeyPath = s
			}
		} else {
			slog.Warn("failed to load git config from database, using in-memory", "error", lerr)
		}
	}
	if token == "" && r.Config != nil {
		token = r.Config.Git.DefaultToken
	}
	if sshKeyPath == "" && r.Config != nil {
		sshKeyPath = r.Config.Git.SSHKeyPath
	}
	return token, sshKeyPath, nil
}

// syncJobOp picks the resolution.Op constant for sync jobs enqueued via
// runSyncLLMJob. The mapping mirrors how the orchestrator routes
// per-subsystem RPCs (linking, requirements, contracts, qa, …) — same
// provider, just a different transport.
func syncJobOp(subsystem llm.Subsystem, jobType string) string {
	switch subsystem {
	case llm.SubsystemRequirements:
		if strings.Contains(jobType, "extract") {
			return resolution.OpRequirementsExtract
		}
		return resolution.OpRequirementsEnrich
	case llm.SubsystemLinking:
		// Linking jobs run analysis-style RPCs; OpAnalysis routes to
		// the same provider as the workspace summary model.
		return resolution.OpAnalysis
	case llm.SubsystemContracts:
		return resolution.OpAnalysis
	case llm.SubsystemQA:
		switch jobType {
		case "qa.classify":
			return resolution.OpQAClassify
		case "qa.decompose":
			return resolution.OpQADecompose
		case "qa.deep_synth", "qa.synth":
			return resolution.OpQASynth
		case "qa.agent_turn":
			return resolution.OpQAAgentTurn
		default:
			return resolution.OpQASynth
		}
	case llm.SubsystemReasoning:
		return resolution.OpDiscussion
	case llm.SubsystemKnowledge:
		return resolution.OpKnowledge
	}
	return resolution.OpAnalysis
}

// livingWikiOpForJobType maps a living-wiki orchestrator job_type to
// the corresponding resolution.Op* constant for the LLM resolver. R3
// slice 3: cold-start vs regen vs retry-excluded each route through
// the same op family for provider attribution.
func livingWikiOpForJobType(jobType string) string {
	switch jobType {
	case "living_wiki_regen", "living_wiki_assembly":
		return resolution.OpLivingWikiRegen
	default:
		return resolution.OpLivingWikiColdStart
	}
}

// resolveLLMProviderForOp returns the LLM provider name (e.g. "anthropic",
// "openai", "ollama") for the (repoID, op) pair. Used by every
// LLM-backed EnqueueRequest construction so the resulting Job records
// llm_provider for the Monitor page and per-provider metrics.
//
// Returns empty string when the resolver is unavailable or fails. The
// orchestrator's R3 followups B1 hard-block (orchestrator.ErrLLMProviderRequired)
// will refuse the enqueue at that point — by design. An empty provider
// at this layer means a misconfigured deployment (no /admin/llm save +
// no SOURCEBRIDGE_LLM_PROVIDER env bootstrap) and the system fails
// closed rather than silently producing an unattributable job.
//
// op is one of the resolution.Op* constants (see
// internal/llm/resolution/ops.go).
func (r *Resolver) resolveLLMProviderForOp(ctx context.Context, repoID, op string) string {
	if r == nil || r.LLMResolver == nil {
		return ""
	}
	snap, err := r.LLMResolver.Resolve(ctx, repoID, op)
	if err != nil {
		// Don't paper over the resolver error here — return empty so
		// the orchestrator's hard-block fires. The resolver's own log
		// line tells operators what went wrong; the orchestrator's
		// ErrLLMProviderRequired tells the caller the enqueue refused.
		return ""
	}
	return snap.Provider
}

// publishEvent safely publishes to the event bus if available.
func (r *Resolver) publishEvent(eventType string, data map[string]interface{}) {
	if r.EventBus != nil {
		r.EventBus.Publish(events.NewEvent(eventType, data))
	}
}

// livingWikiBroker returns a credentials.Broker backed by the resolver's
// LivingWikiResolver. Returns nil when the resolver is not configured (the
// cold-start runner degrades gracefully by skipping the sink dispatch phase).
func (r *Resolver) livingWikiBroker() credentials.Broker {
	if r.LivingWikiResolver == nil {
		return nil
	}
	return credentials.NewResolverBroker(r.LivingWikiResolver)
}

type resolvedCapabilities struct {
	features        *Features
	ideCapabilities *IDECapabilities
}

// featureToCapability maps an entitlements.Feature constant to its
// corresponding capability-registry name (see capabilities.Registry).
//
// Semantic note: the two systems gate along DIFFERENT axes.
// entitlements.Checker operates on plan level (OSS → free → team → enterprise).
// capabilities.IsAvailable operates on edition (OSS vs enterprise).
// A full 1:1 migration would collapse the four-tier plan model to two
// edition tiers, silently granting team-only features to every enterprise
// user and denying free-plan features to OSS users. That is a behavior
// change, so this adapter is deliberately conservative:
//
//   - Features that have a semantically equivalent capability-registry entry
//     (same gating semantics, verified by inspection) return the capability
//     name. resolveCapabilities routes them through capabilities.IsAvailable.
//   - Features that are ONLY plan-gated (multi-tenant, SSO, connectors, …)
//     return "" and resolveCapabilities keeps using entitlements.IsAllowed.
//
// As the two systems converge (tracked in STRUCT-2), entries can be promoted
// from "" to a capability name here, or new Registry entries added to mirror
// the plan-level gates.
//
// Preserved public surface: entitlements.NewChecker and IsAllowed remain
// available for external consumers and tests — this adapter only affects
// how resolveCapabilities routes its internal queries.
func featureToCapability(feature entitlements.Feature) string {
	// No entitlements.Feature currently has a semantically equivalent
	// capability-registry entry. SubsystemClustering and AgentSetup are
	// capabilities-only (no corresponding Feature constant); they are
	// queried via capabilities.IsAvailable(capabilities.Cap*, edition)
	// directly in resolveCapabilities below.
	//
	// Extension point: when a Feature gains a semantically equivalent
	// capability-registry counterpart, add a case here and remove the
	// entitlements check from resolveCapabilities. Example:
	//   case entitlements.FeatureSSO:
	//       return capabilities.CapSSOIdentity
	_ = feature // suppress unused-parameter warning while the switch is empty
	return ""
}

func (r *Resolver) resolveCapabilities() resolvedCapabilities {
	// Normalize zero-value Plan to PlanOSS so direct resolver construction
	// (tests, extensions) matches the pre-Slice-3 behavior where currentPlan()
	// returned PlanOSS when no env var was set. Without this, a zero-value
	// Resolver would compute Billing: true (r.Plan "" != entitlements.PlanOSS
	// "oss") — incorrect for the no-plan / OSS case.
	plan := r.Plan
	if plan == "" {
		plan = entitlements.PlanOSS
	}

	hasWorker := r.Worker != nil
	hasKnowledge := r.KnowledgeStore != nil
	checker := entitlements.NewChecker(plan)

	// allow gates a plan-level feature through the entitlements checker.
	// When featureToCapability returns a non-empty capability name for
	// the feature, route through capabilities.IsAvailable instead so
	// the resolver path is unified. Today no Feature has a registry
	// counterpart (see featureToCapability), so this always falls through
	// to the entitlements path — the structure is the extension point.
	edition := capabilities.NormalizeEdition(string(plan))
	allow := func(feature entitlements.Feature) bool {
		if cap := featureToCapability(feature); cap != "" {
			return capabilities.IsAvailable(cap, edition)
		}
		return checker.IsAllowed(feature).Allowed
	}

	repoKnowledge := hasKnowledge && hasWorker && (allow(entitlements.FeatureCliffNotes) ||
		allow(entitlements.FeatureLearningPaths) ||
		allow(entitlements.FeatureCodeTours) ||
		allow(entitlements.FeatureSystemExplain))
	scopedKnowledge := repoKnowledge
	scopedExplain := hasWorker && allow(entitlements.FeatureSystemExplain)
	impactReports := true
	discussCode := hasWorker
	reviewCode := hasWorker

	return resolvedCapabilities{
		features: &Features{
			MultiTenant:     allow(entitlements.FeatureMultiTenant),
			Sso:             allow(entitlements.FeatureSSO),
			LinearConnector: allow(entitlements.FeatureLinearConnector),
			JiraConnector:   allow(entitlements.FeatureJiraConnector),
			GithubApp:       allow(entitlements.FeatureGitHubApp),
			GitlabApp:       allow(entitlements.FeatureGitLabApp),
			AuditLog:        allow(entitlements.FeatureAuditLog),
			Webhooks:        allow(entitlements.FeatureWebhooks),
			CustomTemplates: hasWorker && allow(entitlements.FeatureCustomTemplates),
			Billing:         plan != entitlements.PlanOSS,

			CliffNotes:           hasKnowledge && hasWorker && allow(entitlements.FeatureCliffNotes),
			LearningPaths:        hasKnowledge && hasWorker && allow(entitlements.FeatureLearningPaths),
			CodeTours:            hasKnowledge && hasWorker && allow(entitlements.FeatureCodeTours),
			SystemExplain:        hasKnowledge && hasWorker && allow(entitlements.FeatureSystemExplain),
			SymbolScopedAnalysis: hasKnowledge && hasWorker && allow(entitlements.FeatureCliffNotes),
			// Capability-registry features: no entitlements.Feature constant;
			// queried by typed constant to avoid magic strings.
			SubsystemClustering: capabilities.IsAvailable(capabilities.CapSubsystemClustering, edition),
			AgentSetup:          capabilities.IsAvailable(capabilities.CapAgentSetup, edition),

			MultiAudienceKnowledge:   hasKnowledge && hasWorker && allow(entitlements.FeatureMultiAudienceKnowledge),
			CustomKnowledgeTemplates: hasKnowledge && hasWorker && allow(entitlements.FeatureCustomKnowledgeTemplates),
			AdvancedLearningPaths:    hasKnowledge && hasWorker && allow(entitlements.FeatureAdvancedLearningPaths),
			SlideGeneration:          hasKnowledge && hasWorker && allow(entitlements.FeatureSlideGeneration),
			PodcastGeneration:        hasKnowledge && hasWorker && allow(entitlements.FeaturePodcastGeneration),
			KnowledgeScheduling:      hasKnowledge && allow(entitlements.FeatureKnowledgeScheduling),
			KnowledgeExport:          hasKnowledge && allow(entitlements.FeatureKnowledgeExport),
		},
		ideCapabilities: &IDECapabilities{
			RepoKnowledge:   repoKnowledge,
			ScopedKnowledge: scopedKnowledge,
			ScopedExplain:   scopedExplain,
			ImpactReports:   impactReports,
			DiscussCode:     discussCode,
			ReviewCode:      reviewCode,
			Vscode:          true,
			Jetbrains:       allow(entitlements.FeatureJetBrains),
		},
	}
}

func currentPlan() entitlements.Plan {
	if plan := os.Getenv("SOURCEBRIDGE_PLAN"); plan != "" {
		switch entitlements.Plan(plan) {
		case entitlements.PlanOSS, entitlements.PlanFree, entitlements.PlanTeam, entitlements.PlanEnterprise:
			return entitlements.Plan(plan)
		}
	}

	// Canonicalize the edition string through the capabilities
	// registry's normalizer so the plan decision matches every other
	// surface (MCP, REST, capability filter).
	if capabilities.NormalizeEdition(os.Getenv("SOURCEBRIDGE_EDITION")) == capabilities.EditionEnterprise {
		return entitlements.PlanEnterprise
	}
	return entitlements.PlanOSS
}

// BootCurrentPlan is the exported boot-time entry point for resolving the
// entitlement plan from SOURCEBRIDGE_PLAN / SOURCEBRIDGE_EDITION env vars.
// Called once from rest.NewServer so the resolved plan is stored on the
// Resolver.Plan field and reused for every request — no per-request env reads.
func BootCurrentPlan() entitlements.Plan {
	return currentPlan()
}
