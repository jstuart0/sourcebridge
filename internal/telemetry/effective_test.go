// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package telemetry

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// boolPtr is a test helper that returns a pointer to b.
func boolPtr(b bool) *bool { return &b }

// TestGloballyEnabled_Matrix covers the 8 policy cases specified in the plan.
// Each case asserts both GloballyEnabled and FunnelGloballyEnabled to confirm
// the policy is consistent across both surfaces.
func TestGloballyEnabled_Matrix(t *testing.T) {
	cases := []struct {
		name          string
		cfg           config.TelemetryConfig
		dnt           string // DO_NOT_TRACK env value; "" = unset
		wantGlobal    bool
		wantFunnel    bool
	}{
		{
			// Case 1: all defaults, no env overrides — both on.
			name:       "defaults",
			cfg:        config.TelemetryConfig{Enabled: true, FunnelEnabled: true},
			wantGlobal: true,
			wantFunnel: true,
		},
		{
			// Case 2: SOURCEBRIDGE_TELEMETRY=off (parsed to EnvOverride=&false),
			// config still Enabled:true — both forced off.
			name:       "env-off",
			cfg:        config.TelemetryConfig{Enabled: true, FunnelEnabled: true, EnvOverride: boolPtr(false)},
			wantGlobal: false,
			wantFunnel: false,
		},
		{
			// Case 3: config Enabled=false + SOURCEBRIDGE_TELEMETRY=on (EnvOverride=&true).
			// Env "on" cannot re-enable a config-disabled setting (Decision 5).
			name:       "config-off+env-on",
			cfg:        config.TelemetryConfig{Enabled: false, FunnelEnabled: true, EnvOverride: boolPtr(true)},
			wantGlobal: false,
			wantFunnel: false,
		},
		{
			// Case 4: DO_NOT_TRACK=1 + SOURCEBRIDGE_TELEMETRY=on — DNT wins over env "on".
			name:       "dnt+env-on",
			cfg:        config.TelemetryConfig{Enabled: true, FunnelEnabled: true, EnvOverride: boolPtr(true)},
			dnt:        "1",
			wantGlobal: false,
			wantFunnel: false,
		},
		{
			// Case 5: config funnel_enabled=false + SOURCEBRIDGE_FUNNEL_TELEMETRY=on
			// (FunnelEnvOverride=&true). Install-ping true; funnel false.
			// Env "on" cannot re-enable a config-disabled funnel setting.
			name:       "funnel-off-config+funnel-env-on",
			cfg:        config.TelemetryConfig{Enabled: true, FunnelEnabled: false, FunnelEnvOverride: boolPtr(true)},
			wantGlobal: true,
			wantFunnel: false,
		},
		{
			// Case 6: SOURCEBRIDGE_FUNNEL_TELEMETRY=off alone (FunnelEnvOverride=&false).
			// Install-ping true; funnel false.
			name:       "funnel-off-env",
			cfg:        config.TelemetryConfig{Enabled: true, FunnelEnabled: true, FunnelEnvOverride: boolPtr(false)},
			wantGlobal: true,
			wantFunnel: false,
		},
		{
			// Case 7: config Enabled=false alone — both false.
			name:       "global-off-alone",
			cfg:        config.TelemetryConfig{Enabled: false, FunnelEnabled: true},
			wantGlobal: false,
			wantFunnel: false,
		},
		{
			// Case 8: config Enabled=false + FunnelEnabled=true — global wins, both false.
			name:       "global-off+funnel-true",
			cfg:        config.TelemetryConfig{Enabled: false, FunnelEnabled: true},
			wantGlobal: false,
			wantFunnel: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.dnt != "" {
				t.Setenv("DO_NOT_TRACK", tc.dnt)
			} else {
				t.Setenv("DO_NOT_TRACK", "")
			}

			gotGlobal := GloballyEnabled(tc.cfg)
			gotFunnel := FunnelGloballyEnabled(tc.cfg)

			if gotGlobal != tc.wantGlobal {
				t.Errorf("GloballyEnabled: got %v, want %v", gotGlobal, tc.wantGlobal)
			}
			if gotFunnel != tc.wantFunnel {
				t.Errorf("FunnelGloballyEnabled: got %v, want %v", gotFunnel, tc.wantFunnel)
			}
		})
	}
}
