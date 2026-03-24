// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import "testing"

func TestOSSAudiences(t *testing.T) {
	audiences := OSSAudiences()
	if len(audiences) != 2 {
		t.Fatalf("expected 2 OSS audiences, got %d", len(audiences))
	}
	if audiences[0] != AudienceBeginner || audiences[1] != AudienceDeveloper {
		t.Fatalf("unexpected audiences: %v", audiences)
	}
}

func TestIsOSSAudience(t *testing.T) {
	tests := []struct {
		audience Audience
		want     bool
	}{
		{AudienceBeginner, true},
		{AudienceDeveloper, true},
		{AudienceArchitect, false},
		{AudienceProductManager, false},
		{AudienceExecutive, false},
	}
	for _, tc := range tests {
		if got := IsOSSAudience(tc.audience); got != tc.want {
			t.Errorf("IsOSSAudience(%q) = %v, want %v", tc.audience, got, tc.want)
		}
	}
}

func TestIsValidDepth(t *testing.T) {
	tests := []struct {
		depth Depth
		want  bool
	}{
		{DepthSummary, true},
		{DepthMedium, true},
		{DepthDeep, true},
		{Depth("invalid"), false},
		{Depth(""), false},
	}
	for _, tc := range tests {
		if got := IsValidDepth(tc.depth); got != tc.want {
			t.Errorf("IsValidDepth(%q) = %v, want %v", tc.depth, got, tc.want)
		}
	}
}

func TestValidDepths(t *testing.T) {
	depths := ValidDepths()
	if len(depths) != 3 {
		t.Fatalf("expected 3 depths, got %d", len(depths))
	}
}
