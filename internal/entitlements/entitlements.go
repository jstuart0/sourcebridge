// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package entitlements

// Plan represents a subscription plan.
type Plan string

const (
	PlanOSS        Plan = "oss"
	PlanFree       Plan = "free"
	PlanTeam       Plan = "team"
	PlanEnterprise Plan = "enterprise"
)

// Feature represents a gated feature.
type Feature string

const (
	FeatureMultiTenant      Feature = "multi_tenant"
	FeatureSSO              Feature = "sso"
	FeatureLinearConnector  Feature = "linear_connector"
	FeatureJiraConnector    Feature = "jira_connector"
	FeatureGitHubApp        Feature = "github_app"
	FeatureGitLabApp        Feature = "gitlab_app"
	FeatureAuditLog         Feature = "audit_log"
	FeatureWebhooks         Feature = "webhooks"
	FeatureCustomTemplates  Feature = "custom_templates"
	FeatureJetBrains        Feature = "jetbrains"
	FeatureHelmChart        Feature = "helm_chart"

	// Knowledge engine features — OSS
	FeatureCliffNotes  Feature = "cliff_notes"
	FeatureLearningPaths Feature = "learning_paths"
	FeatureCodeTours   Feature = "code_tours"
	FeatureSystemExplain Feature = "system_explain"

	// Knowledge engine features — deferred enterprise
	FeatureMultiAudienceKnowledge Feature = "multi_audience_knowledge"
	FeatureCustomKnowledgeTemplates Feature = "custom_knowledge_templates"
	FeatureAdvancedLearningPaths Feature = "advanced_learning_paths"
	FeatureSlideGeneration Feature = "slide_generation"
	FeaturePodcastGeneration Feature = "podcast_generation"
	FeatureKnowledgeScheduling Feature = "knowledge_scheduling"
	FeatureKnowledgeExport Feature = "knowledge_export"
)

// Check represents the result of an entitlement check.
type Check struct {
	Allowed        bool   `json:"allowed"`
	RequiredPlan   Plan   `json:"required_plan,omitempty"`
	UpgradeMessage string `json:"upgrade_message,omitempty"`
}

// Checker validates feature access based on plan.
type Checker struct {
	currentPlan Plan
}

// NewChecker creates an entitlement checker for the given plan.
func NewChecker(plan Plan) *Checker {
	return &Checker{currentPlan: plan}
}

// IsAllowed checks if a feature is available on the current plan.
func (c *Checker) IsAllowed(feature Feature) Check {
	required := featurePlans[feature]
	if required == "" {
		// Feature not gated
		return Check{Allowed: true}
	}

	if planLevel(c.currentPlan) >= planLevel(required) {
		return Check{Allowed: true}
	}

	return Check{
		Allowed:        false,
		RequiredPlan:   required,
		UpgradeMessage: "Upgrade to " + string(required) + " plan to access " + string(feature),
	}
}

var featurePlans = map[Feature]Plan{
	FeatureMultiTenant:     PlanTeam,
	FeatureSSO:             PlanTeam,
	FeatureLinearConnector: PlanTeam,
	FeatureJiraConnector:   PlanTeam,
	FeatureGitHubApp:       PlanTeam,
	FeatureGitLabApp:       PlanTeam,
	FeatureAuditLog:        PlanEnterprise,
	FeatureWebhooks:        PlanTeam,
	FeatureCustomTemplates: PlanEnterprise,
	FeatureJetBrains:       PlanTeam,
	FeatureHelmChart:       PlanEnterprise,
	// Knowledge engine — OSS features (available on all plans including OSS)
	// Not gated (absent from map = always allowed)

	// Knowledge engine — enterprise features
	FeatureMultiAudienceKnowledge:   PlanTeam,
	FeatureCustomKnowledgeTemplates: PlanEnterprise,
	FeatureAdvancedLearningPaths:    PlanTeam,
	FeatureSlideGeneration:          PlanEnterprise,
	FeaturePodcastGeneration:        PlanEnterprise,
	FeatureKnowledgeScheduling:      PlanEnterprise,
	FeatureKnowledgeExport:          PlanTeam,
}

func planLevel(p Plan) int {
	switch p {
	case PlanOSS:
		return 0
	case PlanFree:
		return 1
	case PlanTeam:
		return 2
	case PlanEnterprise:
		return 3
	default:
		return 0
	}
}
