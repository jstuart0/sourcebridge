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

// Flags holds backend startup-time feature flags.
type Flags struct {
	RuntimeReconfigure bool
}

// LoadFromEnv resolves backend feature flags from environment variables.
func LoadFromEnv() Flags {
	return Flags{
		RuntimeReconfigure: envEnabled(EnvRuntimeReconfigure),
	}
}

// EnabledNames returns the enabled flag names in stable order for logging/telemetry.
func (f Flags) EnabledNames() []string {
	names := make([]string, 0, 1)
	if f.RuntimeReconfigure {
		names = append(names, "runtime_reconfigure")
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
