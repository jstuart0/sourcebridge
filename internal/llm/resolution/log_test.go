// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// TestLogResolved_NeverIncludesAPIKey is the explicit security
// regression test: the per-call structured log line MUST emit
// api_key_set:bool only, never the raw key. If a future refactor
// accidentally adds the raw key to the log fields, this test fails.
func TestLogResolved_NeverIncludesAPIKey(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	snap := Snapshot{
		Provider: "anthropic",
		BaseURL:  "https://api.anthropic.com",
		APIKey:   "sk-ant-api03-DO-NOT-LOG-THIS-VALUE",
		Model:    "claude-sonnet-4",
		Sources: map[string]Source{
			FieldProvider: SourceWorkspace,
			FieldAPIKey:   SourceWorkspace,
		},
	}
	LogResolved(log, OpDiscussion, "repo-1", snap)

	got := buf.String()
	if strings.Contains(got, "DO-NOT-LOG-THIS-VALUE") {
		t.Errorf("API key leaked into log output: %s", got)
	}
	if !strings.Contains(got, `"api_key_set":true`) {
		t.Errorf("expected api_key_set:true in log output; got: %s", got)
	}
	if !strings.Contains(got, `"sources_api_key":"workspace"`) {
		t.Errorf("expected sources_api_key:workspace in log output; got: %s", got)
	}
}

// TestLogResolved_EmitsAllFieldSources is the slice-4 acceptance test
// for the structured-log contract: every field name in Sources must
// appear in the log line. Operators grep for sources_api_key=workspace
// to verify the workspace fix landed; if a future refactor drops a
// field from the log, this test catches it.
func TestLogResolved_EmitsAllFieldSources(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	snap := Snapshot{
		Provider: "ollama",
		BaseURL:  "http://localhost:11434",
		APIKey:   "",
		Model:    "qwen2.5:32b",
		Sources: map[string]Source{
			FieldProvider:    SourceWorkspace,
			FieldBaseURL:     SourceWorkspace,
			FieldAPIKey:      SourceBuiltin, // empty key, builtin layer
			FieldModel:       SourceWorkspace,
			FieldDraftModel:  SourceBuiltin,
			FieldTimeoutSecs: SourceEnvFallback,
		},
		Version: 7,
	}
	LogResolved(log, OpKnowledge, "test-repo", snap)

	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("log output not valid JSON: %v\noutput=%s", err, buf.String())
	}
	for _, want := range []string{
		"sources_provider", "sources_base_url", "sources_api_key",
		"sources_model", "sources_draft_model", "sources_timeout_secs",
	} {
		if _, ok := parsed[want]; !ok {
			t.Errorf("expected field %q in log output; got: %v", want, parsed)
		}
	}
	if got := parsed["sources_provider"]; got != "workspace" {
		t.Errorf("sources_provider: got %v, want workspace", got)
	}
	if got := parsed["api_key_set"]; got != false {
		t.Errorf("api_key_set: got %v, want false", got)
	}
	if got, ok := parsed["version"].(float64); !ok || uint64(got) != 7 {
		t.Errorf("version: got %v, want 7", parsed["version"])
	}
}

// TestResolve_FullFieldMatrix exercises every layer-mix the per-field
// source map advertises. Mirrors the slice-4 acceptance matrix in the
// plan so a reviewer can read this single test and verify the contract.
func TestResolve_FullFieldMatrix(t *testing.T) {
	tests := []struct {
		name         string
		env          config.LLMConfig
		dbRecord     *WorkspaceRecord
		repoOverride *RepoOverride
		op           string
		expect       map[string]Source
	}{
		{
			name: "all-env-no-db",
			env: config.LLMConfig{
				Provider: "anthropic", APIKey: "envk",
				BaseURL: "https://api.anthropic.com",
				SummaryModel: "claude-sonnet", DraftModel: "claude-haiku",
				TimeoutSecs: 90,
			},
			op: OpDiscussion,
			expect: map[string]Source{
				FieldProvider:    SourceEnvFallback,
				FieldBaseURL:     SourceEnvFallback,
				FieldAPIKey:      SourceEnvFallback,
				FieldModel:       SourceEnvFallback,
				FieldDraftModel:  SourceEnvFallback,
				FieldTimeoutSecs: SourceEnvFallback,
			},
		},
		{
			name: "all-db-no-env",
			env:  config.LLMConfig{},
			dbRecord: &WorkspaceRecord{
				Provider: "openai", BaseURL: "https://api.openai.com",
				APIKey: "wsk", SummaryModel: "gpt-4o",
				DraftModel: "gpt-4o-mini", TimeoutSecs: 60,
				Version: 1,
			},
			op: OpDiscussion,
			expect: map[string]Source{
				FieldProvider:    SourceWorkspace,
				FieldBaseURL:     SourceWorkspace,
				FieldAPIKey:      SourceWorkspace,
				FieldModel:       SourceWorkspace,
				FieldDraftModel:  SourceWorkspace,
				FieldTimeoutSecs: SourceWorkspace,
			},
		},
		{
			name: "mixed-env-provider-db-key",
			env: config.LLMConfig{
				Provider: "anthropic", SummaryModel: "claude-sonnet-4",
			},
			dbRecord: &WorkspaceRecord{APIKey: "wsk", Version: 1},
			op:       OpDiscussion,
			expect: map[string]Source{
				FieldProvider:   SourceEnvFallback,
				FieldAPIKey:     SourceWorkspace,
				FieldModel:      SourceEnvFallback,
				FieldDraftModel: SourceBuiltin,
			},
		},
		{
			name: "all-builtin-empty-everywhere",
			env:  config.LLMConfig{},
			op:   OpDiscussion,
			expect: map[string]Source{
				FieldProvider:    SourceBuiltin,
				FieldBaseURL:     SourceBuiltin,
				FieldAPIKey:      SourceBuiltin,
				FieldModel:       SourceBuiltin,
				FieldDraftModel:  SourceBuiltin,
				FieldTimeoutSecs: SourceBuiltin,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var store LLMConfigStore
			if tt.dbRecord != nil {
				fs := &fakeStore{rec: tt.dbRecord, version: tt.dbRecord.Version}
				store = fs
			}
			r := New(store, nil, tt.env, nil)
			snap, err := r.Resolve(context.Background(), "", tt.op)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			for field, want := range tt.expect {
				if got := snap.Sources[field]; got != want {
					t.Errorf("Sources[%s]: got %q, want %q", field, got, want)
				}
			}
		})
	}
}
