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
	"github.com/sourcebridge/sourcebridge/internal/entitlements"
	"github.com/sourcebridge/sourcebridge/internal/events"
	gitres "github.com/sourcebridge/sourcebridge/internal/git/resolution"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

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

// Resolver holds the GraphQL resolver's dependencies.
//
// Construction convention (Decision 6 / CA-184): tests MUST include
// Deps: &appdeps.AppDeps{...} populated with the fields the test uses.
// A zero-value &Resolver{} will nil-deref on first method call that reads
// r.Deps.X. The production constructor (internal/api/rest/router.go) always
// sets Deps = s.AppDeps.
//
// Field set is pinned by TestResolverStructureCanary in internal/appdeps/appdeps_test.go.
// Adding a new subsystem dependency: add a field to appdeps.AppDeps — the
// graphql.Resolver side reads via r.Deps.<Field> automatically. No resolver-side
// wiring step required.
type Resolver struct {
	// Deps is the shared dependency registry. All subsystem dependencies
	// (stores, clients, orchestrators, etc.) are read via r.Deps.<Field>.
	// See internal/appdeps for the canonical registry and the full field list.
	Deps *appdeps.AppDeps

	// Store is the per-tenant filtered store, not in AppDeps because it is
	// per-request (TenantFilteredStore pattern). See getStore(ctx).
	Store graph.GraphStore

	// Plan is the entitlement plan resolved at boot via BootCurrentPlan().
	// Per-request capability resolution reads this field instead of calling
	// currentPlan() (which reads env vars) on every GraphQL request.
	// Zero value normalized to PlanOSS at the resolveCapabilities call site.
	Plan entitlements.Plan

	// ClusteringHook is called after each successful index run with (repoID,
	// commitSHA) to enqueue an async clustering job. Nil = clustering disabled.
	// Not in AppDeps: this is a closure constructed at wiring time; see
	// the appdeps package doc for rationale.
	ClusteringHook func(repoID, commitSHA string)
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
// When r.Deps.GitResolver is nil (e.g. test wiring that supplies only
// Deps.Config), the env-bootstrap values in cfg.Git are returned directly.
//
// resolveGitCredentialsForOp(ctx, "<op>") so the structured log line
// carries op context. This zero-arg form is kept for callers that do not
// need an op label, asserted as allowlisted in
// internal/git/resolution/lint_test.go (resolveGitCredentials).
//
//nolint:unused // Some callers use the named-op variant; both are allowlisted.
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
	if r.Deps.GitResolver != nil {
		snap, resolveErr := r.Deps.GitResolver.Resolve(ctx)
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

	// Env-bootstrap fallback when no resolver is wired (test or embedded mode).
	if r.Deps.Config != nil {
		return r.Deps.Config.Git.DefaultToken, r.Deps.Config.Git.SSHKeyPath, nil
	}
	return "", "", nil
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
	if r == nil || r.Deps.LLMResolver == nil {
		return ""
	}
	snap, err := r.Deps.LLMResolver.Resolve(ctx, repoID, op)
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
	if r.Deps.EventBus != nil {
		r.Deps.EventBus.Publish(events.NewEvent(eventType, data))
	}
}

// livingWikiBroker returns a credentials.Broker backed by the resolver's
// LivingWikiResolver. Returns nil when the resolver is not configured (the
// cold-start runner degrades gracefully by skipping the sink dispatch phase).
func (r *Resolver) livingWikiBroker() credentials.Broker {
	if r.Deps.LivingWikiResolver == nil {
		return nil
	}
	return credentials.NewResolverBroker(r.Deps.LivingWikiResolver)
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
// featureToCapability maps an entitlements.Feature to its semantically
// equivalent capability-registry name, or "" if no equivalent exists.
//
// When the return value is non-empty, resolveCapabilities routes the check
// through capabilities.IsAvailable(cap, edition) instead of the entitlements
// checker. This unifies the two gating axes for features where the semantics
// are genuinely equivalent.
//
// Mapping rationale (update this table when adding or removing entries):
//
//	Feature            Cap             Basis
//	FeatureAuditLog    CapAuditLog     Both enterprise-only; capability registry
//	                                   entry mirrors the entitlements gate
//	                                   exactly. (entitlements.go:91 = PlanEnterprise;
//	                                   registry_data.go:236 = EditionEnterprise)
//
// Near-miss features intentionally NOT mapped (semantic mismatch):
//
//	FeatureSSO, FeatureMultiTenant, connectors, JetBrains — plan-gated on
//	the entitlements axis; no semantically equivalent capability-registry
//	entry exists today. Do not add entries here without verifying the two
//	gating axes agree for every plan/edition combination.
//
// Capabilities-only features (no Feature constant; no entry possible here):
//
//	CapSubsystemClustering, CapAgentSetup — queried directly in
//	resolveCapabilities via capabilities.IsAvailable.
//
// Preserved public surface: entitlements.NewChecker and IsAllowed remain
// available for external consumers and tests — this adapter only affects
// how resolveCapabilities routes its internal queries.
func featureToCapability(feature entitlements.Feature) string {
	switch feature {
	case entitlements.FeatureAuditLog:
		// Enterprise-only in both systems; semantically equivalent.
		// Entitlements: PlanEnterprise (entitlements.go:91).
		// Capability registry: EditionEnterprise (registry_data.go:236).
		return capabilities.CapAuditLog
	default:
		return ""
	}
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

	hasWorker := r.Deps.Worker != nil
	hasKnowledge := r.Deps.KnowledgeStore != nil
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
