// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package featureflags

import (
	"os"
	"sort"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/telemetry"
)

const (
	EnvRuntimeReconfigure = "SOURCEBRIDGE_FEATURE_RUNTIME_RECONFIGURE"

	// EnvGlobalTelemetry is the env variable name for global telemetry control.
	// Parsing happens in internal/config via parseTelemetryEnvToken; this
	// constant is exported for documentation and diagnostic logging.
	EnvGlobalTelemetry = "SOURCEBRIDGE_TELEMETRY"

	// EnvFunnelTelemetry is the env variable name for funnel-specific telemetry
	// control. Same semantics as EnvGlobalTelemetry.
	EnvFunnelTelemetry = "SOURCEBRIDGE_FUNNEL_TELEMETRY"
)

// Flags holds backend startup-time feature flags.
type Flags struct {
	RuntimeReconfigure bool
	// FunnelTelemetry reports whether the local funnel/adoption telemetry
	// surface is active. Determined by the full opt-out chain:
	// DO_NOT_TRACK → SOURCEBRIDGE_TELEMETRY → [telemetry] enabled →
	// SOURCEBRIDGE_FUNNEL_TELEMETRY → [telemetry] funnel_enabled.
	// Never included in EnabledNames() to avoid leaking local state info
	// in the external install ping (Critical C2).
	FunnelTelemetry bool
}

// LoadFromEnv resolves backend feature flags from environment variables,
// using config defaults for telemetry settings. Callers that have a parsed
// config.Config should use LoadFromEnvWithConfig instead so the full
// opt-out chain (including config-file values) is respected.
func LoadFromEnv() Flags {
	return Flags{
		RuntimeReconfigure: envEnabled(EnvRuntimeReconfigure),
		FunnelTelemetry: funnelTelemetryEnabled(config.TelemetryConfig{
			Enabled:       true,
			FunnelEnabled: true,
		}),
	}
}

// LoadFromEnvWithConfig resolves backend feature flags using the full
// config-backed telemetry opt-out chain. Pass cfg.Telemetry from the
// loaded config so both env-token and config-file settings are honoured.
func LoadFromEnvWithConfig(cfg config.TelemetryConfig) Flags {
	return Flags{
		RuntimeReconfigure: envEnabled(EnvRuntimeReconfigure),
		FunnelTelemetry:    funnelTelemetryEnabled(cfg),
	}
}

// EnabledNames returns the enabled flag names in stable order for logging/telemetry.
// FunnelTelemetry is intentionally excluded — it is a local-state flag and
// must not appear in the external install ping (Critical C2).
func (f Flags) EnabledNames() []string {
	names := make([]string, 0, 1)
	if f.RuntimeReconfigure {
		names = append(names, "runtime_reconfigure")
	}
	sort.Strings(names)
	return names
}

// funnelTelemetryEnabled is the funnel surface's view of the shared telemetry
// policy. The full chain (DO_NOT_TRACK, EnvOverride, config.Enabled,
// config.FunnelEnabled, FunnelEnvOverride) is owned by
// telemetry.FunnelGloballyEnabled — this wrapper exists so the featureflags
// package isn't a re-implementation of the policy.
func funnelTelemetryEnabled(cfg config.TelemetryConfig) bool {
	return telemetry.FunnelGloballyEnabled(cfg)
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
