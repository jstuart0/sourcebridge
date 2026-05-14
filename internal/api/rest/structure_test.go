// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"reflect"
	"sort"
	"testing"
)

// TestServerStructureCanary pins the exact field set on rest.Server (CA-328).
//
// After CA-328, Server has one shared-dependency field (Deps) plus REST-only
// fields. The full REST-only field set is intentionally large — these are
// construction helpers, lifecycle state, middleware handles, and other
// things that genuinely belong on the HTTP layer and not in the shared
// AppDeps registry.
//
// If this test fails after you add a new field, ask:
//
//	a. Is this a subsystem dependency (store, client, orchestrator)?
//	   → Add it to appdeps.AppDeps and access via s.Deps.<Field>.
//	b. Is it genuinely REST-only (lifecycle, auth, routing, wiring)?
//	   → Add it to the allowlist below with a comment explaining why.
//
// This canary prevents re-introduction of the mirror-field pattern that
// CA-328 removed. See the CA-328 CLAUDE.md entry for details.
func TestServerStructureCanary(t *testing.T) {
	// All legitimate Server fields. Everything here is either:
	//   - Deps: the shared AppDeps registry
	//   - A REST-only field (construction-time, lifecycle, auth, routing)
	// Never add a field here that belongs in appdeps.AppDeps.
	allowed := map[string]bool{
		// Shared dependency registry (CA-328).
		"Deps": true,

		// Boot-time process config. Kept on Server (not in AppDeps) because it
		// is the primary lifecycle field passed as the first NewServer parameter
		// and is used throughout setupRouter at wiring time.
		"cfg": true,

		// REST-only infrastructure.
		"router":    true,
		"localAuth": true,
		"jwtMgr":    true,
		"oidc":      true,
		"store":     true,

		// jobStore holds the raw llm.JobStore used to build Deps.Orchestrator.
		"jobStore": true,

		// REST-only wiring and option fields.
		"tokenStore":             true,
		"desktopAuth":            true,
		"gitConfigStore":         true,
		"llmConfigStore":         true,
		"llmProfileStore":        true,
		"queueControlStore":      true,
		"enterpriseDB":           true,
		"repoChecker":            true,
		"mcp":                    true,
		"mcpPermChecker":         true,
		"mcpAuditLogger":         true,
		"mcpToolExtender":        true,
		"summaryNodeStore":       true,
		"cache":                  true,
		"workerLanes":            true,
		"searchMetrics":          true,
		"livingWikiDispatcher":   true,
		"knowledgeSettingsStore": true,
		"clusterRunner":          true,
		"workerVersionLookup":    true,
		"gateSnapshotCache":      true,

		// encryptionKeySet: wiring-time bool; stays on Server for the
		// WithEncryptionKeySet option pattern; written to s.Deps at boot.
		"encryptionKeySet": true,

		// Dispatcher and QA-locator are construction-time closures.
		"changeDispatcher": true,
		"qaLocator":        true,

		// Graceful-drain lifecycle state (CA-142).
		"serverDraining": true,
		"drainingAt":     true,
		"drainingMu":     true,
		"OnDemand":       true,
	}

	rt := reflect.TypeOf(Server{})
	var unexpected []string
	for i := range rt.NumField() {
		name := rt.Field(i).Name
		if !allowed[name] {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)

	if len(unexpected) > 0 {
		t.Errorf("rest.Server has unexpected fields: %v\n"+
			"If this is a new subsystem dependency, add it to appdeps.AppDeps and access via s.Deps.<Field>.\n"+
			"If it's legitimately REST-only, add its name to the allowlist in this test with a comment.",
			unexpected)
	}

	// Verify the expected fields are all present (guard against accidental deletion).
	for name := range allowed {
		if _, ok := rt.FieldByName(name); !ok {
			t.Errorf("rest.Server is missing expected field %q", name)
		}
	}
}
