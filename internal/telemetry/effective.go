// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package telemetry

import (
	"os"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// GloballyEnabled is the single shared "is telemetry on?" decision used
// by both surfaces (install ping in this package; funnel via
// featureflags.funnelTelemetryEnabled). It encodes Decision 5: env "on"
// tokens cannot re-enable a config-disabled setting, and DO_NOT_TRACK=1
// forces off regardless.
//
// Evaluation order — every layer can force OFF; only the absence of all
// off-signals returns ON:
//
//  1. DO_NOT_TRACK=1 → false
//  2. cfg.EnvOverride != nil && !*cfg.EnvOverride → false (env "off" wins)
//  3. !cfg.Enabled → false (config-file disabled)
//  4. otherwise → true
//
// Note: env "on" (cfg.EnvOverride == &true) is intentionally a no-op
// here. If cfg.Enabled is false, this function always returns false.
// The *true pointer exists so the config layer can distinguish "explicitly
// set to on" from "unset" for diagnostic logging; the policy layer ignores
// the true case entirely.
func GloballyEnabled(cfg config.TelemetryConfig) bool {
	if os.Getenv("DO_NOT_TRACK") == "1" {
		return false
	}
	if cfg.EnvOverride != nil && !*cfg.EnvOverride {
		return false
	}
	if !cfg.Enabled {
		return false
	}
	return true
}

// FunnelGloballyEnabled extends GloballyEnabled with funnel-specific
// gates. Returns true only when both the global layer and the
// funnel-specific layer permit. Env "on" cannot re-enable; only env
// "off" (FunnelEnvOverride == &false) can override a config-true funnel
// setting downward.
func FunnelGloballyEnabled(cfg config.TelemetryConfig) bool {
	if !GloballyEnabled(cfg) {
		return false
	}
	if cfg.FunnelEnvOverride != nil && !*cfg.FunnelEnvOverride {
		return false
	}
	if !cfg.FunnelEnabled {
		return false
	}
	return true
}
