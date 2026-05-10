package appdeps_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/api/graphql"
)

// TestResolverStructureCanary pins the exact field set on graphql.Resolver.
//
// After CA-305 Phase 2, Resolver has exactly four fields:
//   - Deps           — *appdeps.AppDeps pointer (all subsystem dependencies)
//   - Store          — per-tenant filtered store (not in AppDeps; per-request lifecycle)
//   - Plan           — boot-time entitlement plan (not in AppDeps; computed at boot)
//   - ClusteringHook — wiring-time closure (excluded from AppDeps by design)
//
// If this test fails after you add a new field to Resolver, either:
//
//	a. The field belongs in appdeps.AppDeps (canonical path) — add it there
//	   and update no canary; the resolver reads r.Deps.<Field> automatically.
//	b. The field is legitimately resolver-only (like Store, Plan, ClusteringHook)
//	   — add it to the allowlist below and document why it doesn't belong in AppDeps.
//
// This canary prevents re-introduction of the 26-field mirror pattern that
// CA-184 removed. See thoughts/shared/plans/active-2026-05-10-deliver-resolver-appdeps-dedup.md.
func TestResolverStructureCanary(t *testing.T) {
	allowed := map[string]bool{
		"Deps":           true,
		"Store":          true,
		"Plan":           true,
		"ClusteringHook": true,
	}

	rt := reflect.TypeOf(graphql.Resolver{})
	var unexpected []string
	for i := range rt.NumField() {
		name := rt.Field(i).Name
		if !allowed[name] {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)

	if len(unexpected) > 0 {
		t.Errorf("graphql.Resolver has unexpected fields: %v\n"+
			"If this is a new subsystem dependency, add it to appdeps.AppDeps instead.\n"+
			"If it's legitimately resolver-only, add its name to the allowlist in this test.",
			unexpected)
	}

	// Also verify the expected fields are all present (guard against accidental deletion).
	for name := range allowed {
		if _, ok := rt.FieldByName(name); !ok {
			t.Errorf("graphql.Resolver is missing expected field %q", name)
		}
	}
}
