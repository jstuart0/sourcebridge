package graphql

import (
	"context"
	"log/slog"
	"os"

	"github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/entitlements"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/health"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/governance"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/search"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/sourcebridge/sourcebridge/internal/trash"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

// GitConfigLoader reads git credentials from persistent storage.
type GitConfigLoader interface {
	LoadGitConfig() (token, sshKeyPath string, err error)
}

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require here.

type Resolver struct {
	Store              graph.GraphStore
	KnowledgeStore     knowledge.KnowledgeStore   // nil when knowledge persistence is unavailable
	Worker             *worker.Client             // nil when AI features are unavailable
	Orchestrator       *orchestrator.Orchestrator // nil when llm orchestration is unavailable (degraded mode)
	Config             *config.Config             // application configuration
	EventBus           *events.Bus                // in-process event bus for SSE notifications
	Flags              featureflags.Flags         // backend startup-time feature flags
	GitConfig          GitConfigLoader            // reads git credentials from DB (multi-replica safe)
	ComprehensionStore comprehension.Store        // comprehension settings + model capabilities; nil when unavailable
	HealthChecker      *health.Checker            // shared DB+worker health probe; nil = no live checks (embedded/test mode)
	LivingWikiStore              livingwiki.Store              // living-wiki UI settings; nil when unavailable
	LivingWikiResolver           *livingwiki.Resolver          // resolved living-wiki settings (UI + env fallback)
	LivingWikiRepoStore          livingwiki.RepoSettingsStore  // per-repo living-wiki opt-in; nil when unavailable
	LivingWikiJobResultStore     livingwiki.JobResultStore     // per-run job result history; nil when unavailable
	LivingWikiLiveOrchestrator   *lworch.Orchestrator          // living-wiki page-generation orchestrator; nil when feature unavailable
	TrashStore                   trash.Store                   // soft-delete recycle bin; nil when the feature is disabled or unavailable
	QA                 *qa.Orchestrator           // server-side deep-QA orchestrator; nil when server-side QA is disabled
	SearchSvc          *search.Service            // hybrid retrieval backbone; nil falls back to legacy substring search
	ReqBooster         *search.RequirementBooster // requirement-link cache; link mutations call Invalidate so subsequent searches see fresh links
	LivingWikiAuditLog governance.AuditLog        // audit trail for credential rotations and settings changes; nil disables audit logging
	// ClusteringHook is called after each successful index run with (repoID,
	// commitSHA) to enqueue an async clustering job. Nil = clustering disabled.
	ClusteringHook func(repoID, commitSHA string)
	// ClusterStore provides cluster lookups for Living Wiki taxonomy resolution.
	// When nil, resolveTaxonomy falls back to the package-path heuristic.
	ClusterStore clustering.ClusterStore
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

// resolveGitCredentials returns the current git token and SSH key path,
// reading from the database for multi-replica consistency. Falls back to
// the in-memory config if no database store is available.
func (r *Resolver) resolveGitCredentials() (token, sshKeyPath string) {
	if r.GitConfig != nil {
		if t, s, err := r.GitConfig.LoadGitConfig(); err == nil {
			if t != "" {
				token = t
			}
			if s != "" {
				sshKeyPath = s
			}
		} else {
			slog.Warn("failed to load git config from database, using in-memory", "error", err)
		}
	}
	// Fall back to in-memory config for anything the DB didn't provide
	if token == "" && r.Config != nil {
		token = r.Config.Git.DefaultToken
	}
	if sshKeyPath == "" && r.Config != nil {
		sshKeyPath = r.Config.Git.SSHKeyPath
	}
	return
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

func (r *Resolver) resolveCapabilities() resolvedCapabilities {
	hasWorker := r.Worker != nil
	hasKnowledge := r.KnowledgeStore != nil
	checker := entitlements.NewChecker(currentPlan())

	allow := func(feature entitlements.Feature) bool {
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
			Billing:         currentPlan() != entitlements.PlanOSS,

			CliffNotes:           hasKnowledge && hasWorker && allow(entitlements.FeatureCliffNotes),
			LearningPaths:        hasKnowledge && hasWorker && allow(entitlements.FeatureLearningPaths),
			CodeTours:            hasKnowledge && hasWorker && allow(entitlements.FeatureCodeTours),
			SystemExplain:        hasKnowledge && hasWorker && allow(entitlements.FeatureSystemExplain),
			SymbolScopedAnalysis: hasKnowledge && hasWorker && allow(entitlements.FeatureCliffNotes),
			SubsystemClustering:  capabilities.IsAvailable("subsystem_clustering", capabilities.NormalizeEdition(string(currentPlan()))),
			AgentSetup:           capabilities.IsAvailable("agent_setup", capabilities.NormalizeEdition(string(currentPlan()))),

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
