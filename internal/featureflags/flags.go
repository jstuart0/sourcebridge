// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package featureflags

import (
	"os"
	"sort"
	"strings"
)

const (
	EnvRuntimeReconfigure = "SOURCEBRIDGE_FEATURE_RUNTIME_RECONFIGURE"
)

const (
	EnvLivingWikiKillSwitch         = "SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH"
	EnvSelectiveInvalidation        = "SOURCEBRIDGE_SELECTIVE_INVALIDATION"
	EnvKnowledgePrewarmOnIndex      = "SOURCEBRIDGE_KNOWLEDGE_PREWARM_ON_INDEX"
	EnvDebugEndpoints               = "SOURCEBRIDGE_DEBUG_ENDPOINTS"
)

// Flags holds backend startup-time feature flags.
type Flags struct {
	RuntimeReconfigure bool

	// LivingWikiKillSwitch pauses all living-wiki job dispatch when true.
	// Corresponds to SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH. Defaults to false.
	LivingWikiKillSwitch bool

	// SelectiveInvalidationEnabled toggles Phase 1 selective knowledge
	// artifact invalidation on reindex. When false, the legacy blanket
	// MarkAllStale behavior is used. Corresponds to SOURCEBRIDGE_SELECTIVE_INVALIDATION.
	// Defaults to true (opt-out flag).
	SelectiveInvalidationEnabled bool

	// KnowledgePrewarmOnIndexEnabled controls whether a background
	// seedRepositoryFieldGuide goroutine fires after each reindex.
	// Corresponds to SOURCEBRIDGE_KNOWLEDGE_PREWARM_ON_INDEX. Defaults to true (opt-out flag).
	KnowledgePrewarmOnIndexEnabled bool

	// DebugEndpointsEnabled registers internal debug routes (e.g.
	// /api/v1/admin/debug/slow-job) when true. Corresponds to
	// SOURCEBRIDGE_DEBUG_ENDPOINTS. Defaults to false.
	DebugEndpointsEnabled bool
}

// LoadFromEnv resolves backend feature flags from environment variables.
func LoadFromEnv() Flags {
	return Flags{
		RuntimeReconfigure:             envEnabled(EnvRuntimeReconfigure),
		LivingWikiKillSwitch:           envEnabled(EnvLivingWikiKillSwitch),
		SelectiveInvalidationEnabled:   envEnabledDefaultTrue(EnvSelectiveInvalidation),
		KnowledgePrewarmOnIndexEnabled: envEnabledDefaultTrue(EnvKnowledgePrewarmOnIndex),
		DebugEndpointsEnabled:          envEnabled(EnvDebugEndpoints),
	}
}

// EnabledNames returns the enabled flag names in stable order for logging/telemetry.
func (f Flags) EnabledNames() []string {
	names := make([]string, 0, 5)
	if f.RuntimeReconfigure {
		names = append(names, "runtime_reconfigure")
	}
	if f.LivingWikiKillSwitch {
		names = append(names, "living_wiki_kill_switch")
	}
	if f.SelectiveInvalidationEnabled {
		names = append(names, "selective_invalidation")
	}
	if f.KnowledgePrewarmOnIndexEnabled {
		names = append(names, "knowledge_prewarm_on_index")
	}
	if f.DebugEndpointsEnabled {
		names = append(names, "debug_endpoints")
	}
	sort.Strings(names)
	return names
}

func envEnabled(name string) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// envEnabledDefaultTrue is the inverse of envEnabled: the flag is on unless
// the env var is explicitly set to a falsy value. Used for opt-out flags
// (selective invalidation, knowledge prewarm) that are enabled by default.
func envEnabledDefaultTrue(name string) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	switch raw {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
