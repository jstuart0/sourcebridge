package graphql

import (
	"log/slog"

	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

func (r *Resolver) applyEffectiveComprehensionSettings(eff *comprehension.EffectiveSettings) {
	if eff == nil || r.Orchestrator == nil || !r.Flags.RuntimeReconfigure {
		return
	}
	oldConfigured, newConfigured := r.Orchestrator.ReconfigureMaxConcurrency(eff.MaxConcurrency)
	slog.Info("orchestrator_reconfigure",
		"event", "orchestrator_reconfigure",
		"source", "graphql_update_comprehension_settings",
		"old_configured_pool_size", oldConfigured,
		"new_configured_pool_size", newConfigured)
}
