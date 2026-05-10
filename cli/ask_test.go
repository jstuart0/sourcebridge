// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fakeAskServer captures the last request body sent to /api/v1/ask and always
// returns a minimal valid JSON response.
func fakeAskServer(t *testing.T) (*httptest.Server, *[]byte) {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/ask" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			captured = body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"answer":"test answer","diagnostics":{"mode":"deep"}}`))
			return
		}
		if r.URL.Path == "/healthz" {
			w.Header().Set("X-SourceBridge-QA", "v1")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	return srv, &captured
}

// buildAskCmd constructs a fresh cobra.Command wired to runAsk so that
// cmd.Flags().Changed("mode") works correctly per-test without bleeding
// package-level flag state between tests.
func buildAskCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{
		Use:  "ask [question]",
		Args: cobra.ExactArgs(1),
		RunE: runAsk,
	}
	cmd.Flags().StringVar(&askRepoPath, "repo", ".", "")
	cmd.Flags().BoolVar(&askJSON, "json", false, "")
	cmd.Flags().StringVar(&askMode, "mode", "", "")
	cmd.Flags().StringVar(&askServerURL, "server", "", "")
	cmd.Flags().BoolVar(&askLegacy, "legacy", false, "")
	cmd.Flags().StringVar(&askRepositoryID, "repository-id", "", "")
	return cmd
}

// TestRunAskServer_OmitsModeWhenNotSet verifies that when --mode is not
// passed on the CLI, the JSON request body sent to the server does not
// contain a "mode" key at all (so the server-side pipeline default fires).
func TestRunAskServer_OmitsModeWhenNotSet(t *testing.T) {
	srv, captured := fakeAskServer(t)
	defer srv.Close()

	cmd := buildAskCmd(t)
	// Reset package-level state that may have been set by other tests.
	askMode = ""
	askRepositoryID = "repo-123"

	cmd.SetArgs([]string{
		"--server", srv.URL,
		"--repository-id", "repo-123",
		"what does foo do",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute: %v", err)
	}

	if *captured == nil {
		t.Fatal("no request body captured; /api/v1/ask was not called")
	}

	var body map[string]any
	if err := json.Unmarshal(*captured, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if _, ok := body["mode"]; ok {
		t.Errorf("expected 'mode' key to be absent from request body, but got %q", body["mode"])
	}
}

// TestRunAskServer_SendsExplicitModeFast verifies that when --mode fast is
// passed, the JSON body contains "mode": "fast".
func TestRunAskServer_SendsExplicitModeFast(t *testing.T) {
	srv, captured := fakeAskServer(t)
	defer srv.Close()

	cmd := buildAskCmd(t)
	askRepositoryID = "repo-123"

	cmd.SetArgs([]string{
		"--server", srv.URL,
		"--repository-id", "repo-123",
		"--mode", "fast",
		"what does foo do",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute: %v", err)
	}

	if *captured == nil {
		t.Fatal("no request body captured")
	}

	var body map[string]any
	if err := json.Unmarshal(*captured, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got, ok := body["mode"]; !ok {
		t.Error("expected 'mode' key in request body, but it was absent")
	} else if got != "fast" {
		t.Errorf("expected mode=fast, got %q", got)
	}
}

// TestRunAskServer_SendsExplicitModeDeep verifies that when --mode deep is
// passed, the JSON body contains "mode": "deep".
func TestRunAskServer_SendsExplicitModeDeep(t *testing.T) {
	srv, captured := fakeAskServer(t)
	defer srv.Close()

	cmd := buildAskCmd(t)
	askRepositoryID = "repo-123"

	cmd.SetArgs([]string{
		"--server", srv.URL,
		"--repository-id", "repo-123",
		"--mode", "deep",
		"what does foo do",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute: %v", err)
	}

	if *captured == nil {
		t.Fatal("no request body captured")
	}

	var body map[string]any
	if err := json.Unmarshal(*captured, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got, ok := body["mode"]; !ok {
		t.Error("expected 'mode' key in request body, but it was absent")
	} else if got != "deep" {
		t.Errorf("expected mode=deep, got %q", got)
	}
}

// TestPrintAskPretty_PrefersDiagnosticsModeLabel verifies that printAskPretty
// uses the server's diagnostics.mode when non-empty, even when the package-level
// askMode is set to a different value (pins the pre-Phase-1.5 regression where
// [fast] was printed for responses that actually ran deep).
func TestPrintAskPretty_PrefersDiagnosticsModeLabel(t *testing.T) {
	// Simulate the old state: flag was "fast" (the pre-Phase-1.5 default).
	orig := askMode
	askMode = "fast"
	defer func() { askMode = orig }()

	resp := map[string]any{
		"answer": "the answer",
		"diagnostics": map[string]any{
			"mode": "deep",
		},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	// Redirect os.Stdout so we can capture what printAskPretty writes.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = pw

	printErr := printAskPretty(raw)

	pw.Close()
	os.Stdout = origStdout

	out, _ := io.ReadAll(pr)
	pr.Close()

	if printErr != nil {
		t.Fatalf("printAskPretty: %v", printErr)
	}

	outStr := string(out)
	if !strings.Contains(outStr, "[deep]") {
		t.Errorf("expected output to contain [deep], got:\n%s", outStr)
	}
	if strings.Contains(outStr, "[fast]") {
		t.Errorf("expected output NOT to contain [fast], but it does:\n%s", outStr)
	}
}
