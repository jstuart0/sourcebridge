// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package featureflags

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/telemetry"
)

func TestLoadFromEnv(t *testing.T) {
	t.Setenv(EnvRuntimeReconfigure, "true")

	flags := LoadFromEnv()
	if !flags.RuntimeReconfigure {
		t.Fatal("expected runtime reconfigure flag to be enabled")
	}
}

func TestEnabledNames(t *testing.T) {
	flags := Flags{RuntimeReconfigure: true}
	names := flags.EnabledNames()
	if len(names) != 1 || names[0] != "runtime_reconfigure" {
		t.Fatalf("unexpected enabled names: %#v", names)
	}
}

// TestEnabledNames_NoFunnelTelemetry asserts that EnabledNames never includes
// "funnel_telemetry" even when the flag is true (Critical C2: funnel state
// must not leak into the external install ping).
func TestEnabledNames_NoFunnelTelemetry(t *testing.T) {
	flags := Flags{RuntimeReconfigure: false, FunnelTelemetry: true}
	for _, name := range flags.EnabledNames() {
		if name == "funnel_telemetry" {
			t.Fatal("EnabledNames must not contain funnel_telemetry")
		}
	}
}

// TestFunnelTelemetryEnabled_IsWrapper asserts that funnelTelemetryEnabled
// returns the same result as telemetry.FunnelGloballyEnabled for three
// representative cases. The exhaustive policy matrix lives in
// internal/telemetry/effective_test.go.
func TestFunnelTelemetryEnabled_IsWrapper(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.TelemetryConfig
		want bool
	}{
		{
			name: "all-on",
			cfg:  config.TelemetryConfig{Enabled: true, FunnelEnabled: true},
			want: true,
		},
		{
			name: "config-off",
			cfg:  config.TelemetryConfig{Enabled: false, FunnelEnabled: true},
			want: false,
		},
		{
			name: "env-off",
			cfg: config.TelemetryConfig{
				Enabled:       true,
				FunnelEnabled: true,
				EnvOverride:   func() *bool { f := false; return &f }(),
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Ensure DO_NOT_TRACK is unset so it doesn't bleed across subtests.
			t.Setenv("DO_NOT_TRACK", "")

			got := funnelTelemetryEnabled(tc.cfg)
			gotDirect := telemetry.FunnelGloballyEnabled(tc.cfg)

			if got != tc.want {
				t.Errorf("funnelTelemetryEnabled: got %v, want %v", got, tc.want)
			}
			if got != gotDirect {
				t.Errorf("funnelTelemetryEnabled diverges from telemetry.FunnelGloballyEnabled: %v vs %v", got, gotDirect)
			}
		})
	}
}

// TestLoadFromEnvWithConfig_FunnelOff verifies that LoadFromEnvWithConfig
// honours a config-disabled funnel setting.
func TestLoadFromEnvWithConfig_FunnelOff(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")

	cfg := config.TelemetryConfig{Enabled: true, FunnelEnabled: false}
	flags := LoadFromEnvWithConfig(cfg)
	if flags.FunnelTelemetry {
		t.Fatal("expected FunnelTelemetry to be false when config FunnelEnabled=false")
	}
}
