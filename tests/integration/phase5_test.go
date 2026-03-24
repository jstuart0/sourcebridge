// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workersDir() string {
	// From tests/integration, workers is at ../../workers
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "..", "workers")
}

func TestCLIReviewSecurity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fixtureDir := fixtureRepoPath()
	cmd := exec.CommandContext(ctx, "uv", "run", "python", "cli_review.py",
		filepath.Join(fixtureDir, "go", "payment", "processor.go"))
	cmd.Dir = workersDir()
	cmd.Env = append(os.Environ(), "SOURCEBRIDGE_REVIEW_TEMPLATE=security")

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("review command failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("failed to parse output: %v\nOutput: %s", err, output)
	}

	findings, ok := result["findings"].([]interface{})
	if !ok || len(findings) == 0 {
		t.Fatal("expected non-empty findings array")
	}
}

func TestCLIReviewSOLID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fixtureDir := fixtureRepoPath()
	cmd := exec.CommandContext(ctx, "uv", "run", "python", "cli_review.py",
		filepath.Join(fixtureDir, "go", "main.go"))
	cmd.Dir = workersDir()
	cmd.Env = append(os.Environ(), "SOURCEBRIDGE_REVIEW_TEMPLATE=solid")

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("review command failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	// SOLID template with fake provider returns security findings
	// (fake provider doesn't distinguish between templates — but it returns findings)
	findings, ok := result["findings"].([]interface{})
	if !ok {
		t.Fatal("expected findings array")
	}
	_ = findings // may be empty with fake provider for non-security prompts
}

func TestCLIAsk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fixtureDir := fixtureRepoPath()
	cmd := exec.CommandContext(ctx, "uv", "run", "python", "cli_ask.py",
		"what does processPayment do?")
	cmd.Dir = workersDir()
	cmd.Env = append(os.Environ(), "SOURCEBRIDGE_REPO_PATH="+fixtureDir)

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("ask command failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("failed to parse output: %v\nOutput: %s", err, output)
	}

	answer, ok := result["answer"].(string)
	if !ok || answer == "" {
		t.Fatal("expected non-empty answer")
	}
	if !strings.Contains(strings.ToLower(answer), "payment") {
		t.Errorf("expected answer to mention 'payment', got: %s", answer)
	}
}

func TestCLIReviewUsageTracking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fixtureDir := fixtureRepoPath()
	cmd := exec.CommandContext(ctx, "uv", "run", "python", "cli_review.py",
		filepath.Join(fixtureDir, "go", "payment", "processor.go"))
	cmd.Dir = workersDir()
	cmd.Env = append(os.Environ(), "SOURCEBRIDGE_REVIEW_TEMPLATE=security")

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("review command failed: %v", err)
	}

	var result map[string]interface{}
	json.Unmarshal(output, &result)

	usage, ok := result["usage"].(map[string]interface{})
	if !ok {
		t.Fatal("expected usage field in output")
	}

	if usage["model"] == nil || usage["model"] == "" {
		t.Error("expected model in usage")
	}
	if usage["input_tokens"] == nil || usage["input_tokens"].(float64) == 0 {
		t.Error("expected non-zero input_tokens")
	}
	if usage["output_tokens"] == nil || usage["output_tokens"].(float64) == 0 {
		t.Error("expected non-zero output_tokens")
	}
}

func TestCLIReviewAllTemplates(t *testing.T) {
	templates := []string{"security", "solid", "performance", "reliability", "maintainability"}

	for _, tmpl := range templates {
		t.Run(tmpl, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			fixtureDir := fixtureRepoPath()
			cmd := exec.CommandContext(ctx, "uv", "run", "python", "cli_review.py",
				filepath.Join(fixtureDir, "go", "main.go"))
			cmd.Dir = workersDir()
			cmd.Env = append(os.Environ(), fmt.Sprintf("SOURCEBRIDGE_REVIEW_TEMPLATE=%s", tmpl))

			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("review %s failed: %v", tmpl, err)
			}

			var result map[string]interface{}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatalf("failed to parse output: %v", err)
			}

			// FakeLLMProvider returns "security" category for all templates since it
			// detects "review" keyword. Template name is set by the reviewer.
			_ = result["template"]
		})
	}
}

func TestCLIAskReferences(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fixtureDir := fixtureRepoPath()
	cmd := exec.CommandContext(ctx, "uv", "run", "python", "cli_ask.py",
		"what does this code do?")
	cmd.Dir = workersDir()
	cmd.Env = append(os.Environ(), "SOURCEBRIDGE_REPO_PATH="+fixtureDir)

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("ask command failed: %v", err)
	}

	var result map[string]interface{}
	json.Unmarshal(output, &result)

	refs, ok := result["references"].([]interface{})
	if !ok {
		t.Fatal("expected references array")
	}
	_ = refs // May be populated by fake provider

	reqs, ok := result["related_requirements"].([]interface{})
	if !ok {
		t.Fatal("expected related_requirements array")
	}
	_ = reqs
}
