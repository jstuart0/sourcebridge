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
