// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package featureflags

import "testing"

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

// TestFeatureFlagsLoadFromEnv verifies all four new flag fields load correctly
// from their corresponding environment variables. Covers both default-false
// (kill-switch, debug-endpoints) and default-true (selective-invalidation,
// prewarm) polarities.
func TestFeatureFlagsLoadFromEnv(t *testing.T) {
	t.Run("kill_switch_enabled", func(t *testing.T) {
		t.Setenv(EnvLivingWikiKillSwitch, "true")
		f := LoadFromEnv()
		if !f.LivingWikiKillSwitch {
			t.Fatal("expected LivingWikiKillSwitch=true when env set to 'true'")
		}
	})

	t.Run("kill_switch_default_false", func(t *testing.T) {
		// env not set — must default to false
		f := LoadFromEnv()
		if f.LivingWikiKillSwitch {
			t.Fatal("expected LivingWikiKillSwitch=false when env unset")
		}
	})

	t.Run("selective_invalidation_default_true", func(t *testing.T) {
		// env not set — opt-out flag must default to true
		f := LoadFromEnv()
		if !f.SelectiveInvalidationEnabled {
			t.Fatal("expected SelectiveInvalidationEnabled=true when env unset (opt-out flag)")
		}
	})

	t.Run("selective_invalidation_opt_out", func(t *testing.T) {
		t.Setenv(EnvSelectiveInvalidation, "false")
		f := LoadFromEnv()
		if f.SelectiveInvalidationEnabled {
			t.Fatal("expected SelectiveInvalidationEnabled=false when env set to 'false'")
		}
	})

	t.Run("prewarm_default_true", func(t *testing.T) {
		// env not set — opt-out flag must default to true
		f := LoadFromEnv()
		if !f.KnowledgePrewarmOnIndexEnabled {
			t.Fatal("expected KnowledgePrewarmOnIndexEnabled=true when env unset (opt-out flag)")
		}
	})

	t.Run("prewarm_opt_out", func(t *testing.T) {
		t.Setenv(EnvKnowledgePrewarmOnIndex, "0")
		f := LoadFromEnv()
		if f.KnowledgePrewarmOnIndexEnabled {
			t.Fatal("expected KnowledgePrewarmOnIndexEnabled=false when env set to '0'")
		}
	})

	t.Run("debug_endpoints_enabled", func(t *testing.T) {
		t.Setenv(EnvDebugEndpoints, "true")
		f := LoadFromEnv()
		if !f.DebugEndpointsEnabled {
			t.Fatal("expected DebugEndpointsEnabled=true when env set to 'true'")
		}
	})

	t.Run("debug_endpoints_default_false", func(t *testing.T) {
		// env not set — must default to false
		f := LoadFromEnv()
		if f.DebugEndpointsEnabled {
			t.Fatal("expected DebugEndpointsEnabled=false when env unset")
		}
	})
}
