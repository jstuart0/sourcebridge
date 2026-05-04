// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package security_test

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/security"
)

func TestInsecureDefaultWarning_NoneInsecure(t *testing.T) {
	checks := []security.CredentialCheck{
		{Label: "SURREAL_PASS", Value: "my-strong-password"},
		{Label: "JWT_SECRET", Value: "a-real-jwt-secret"},
		{Label: "GRPC_AUTH_SECRET", Value: "a-real-grpc-secret"},
	}
	bad := security.InsecureCredentials(checks)
	if len(bad) != 0 {
		t.Errorf("expected no insecure credentials, got %v", bad)
	}
}

func TestInsecureDefaultWarning_AllInsecure(t *testing.T) {
	checks := []security.CredentialCheck{
		{Label: "SURREAL_PASS", Value: security.InsecureSentinel},
		{Label: "JWT_SECRET", Value: security.InsecureSentinel},
		{Label: "GRPC_AUTH_SECRET", Value: security.InsecureSentinel},
	}
	bad := security.InsecureCredentials(checks)
	if len(bad) != 3 {
		t.Errorf("expected 3 insecure credentials, got %d: %v", len(bad), bad)
	}
	want := map[string]bool{"SURREAL_PASS": true, "JWT_SECRET": true, "GRPC_AUTH_SECRET": true}
	for _, label := range bad {
		if !want[label] {
			t.Errorf("unexpected label in bad list: %q", label)
		}
	}
}

func TestInsecureDefaultWarning_PartialInsecure(t *testing.T) {
	checks := []security.CredentialCheck{
		{Label: "SURREAL_PASS", Value: security.InsecureSentinel},
		{Label: "JWT_SECRET", Value: "a-real-jwt-secret"},
		{Label: "GRPC_AUTH_SECRET", Value: security.InsecureSentinel},
	}
	bad := security.InsecureCredentials(checks)
	if len(bad) != 2 {
		t.Errorf("expected 2 insecure credentials, got %d: %v", len(bad), bad)
	}
	for _, label := range bad {
		if label != "SURREAL_PASS" && label != "GRPC_AUTH_SECRET" {
			t.Errorf("unexpected label: %q", label)
		}
	}
}

func TestInsecureDefaultWarning_OnlySurrealPass(t *testing.T) {
	checks := []security.CredentialCheck{
		{Label: "SURREAL_PASS", Value: security.InsecureSentinel},
		{Label: "JWT_SECRET", Value: "good-secret"},
		{Label: "GRPC_AUTH_SECRET", Value: "good-secret"},
	}
	bad := security.InsecureCredentials(checks)
	if len(bad) != 1 || bad[0] != "SURREAL_PASS" {
		t.Errorf("expected only SURREAL_PASS, got %v", bad)
	}
}

func TestInsecureDefaultWarning_EmptyChecks(t *testing.T) {
	bad := security.InsecureCredentials(nil)
	if len(bad) != 0 {
		t.Errorf("expected empty result for nil checks, got %v", bad)
	}
}
