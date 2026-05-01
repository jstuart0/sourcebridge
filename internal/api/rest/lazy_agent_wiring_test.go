// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"

	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// fakeResolver implements resolution.Resolver for the
// resolverVersionSource test. Returns a version we control.
type fakeResolver struct {
	version atomic.Uint64
	err     error
}

func (f *fakeResolver) Resolve(_ context.Context, _, _ string) (resolution.Snapshot, error) {
	if f.err != nil {
		return resolution.Snapshot{}, f.err
	}
	return resolution.Snapshot{Version: f.version.Load()}, nil
}

func (f *fakeResolver) InvalidateLocal() {}

func TestResolverVersionSource_ReturnsResolverVersionWithOk(t *testing.T) {
	r := &fakeResolver{}
	r.version.Store(42)
	src := &resolverVersionSource{r: r}
	got, ok := src.CurrentWorkspaceVersion(context.Background())
	if got != 42 || !ok {
		t.Errorf("got (%d, %v), want (42, true)", got, ok)
	}
}

func TestResolverVersionSource_NilResolverReturnsNotOk(t *testing.T) {
	src := &resolverVersionSource{r: nil}
	got, ok := src.CurrentWorkspaceVersion(context.Background())
	if got != 0 || ok {
		t.Errorf("got (%d, %v), want (0, false)", got, ok)
	}
}

// TestResolverVersionSource_NilReceiverReturnsNotOk confirms calling
// on a nil pointer is safe — the lazy-probe call site does not
// nil-check the version source, relying on this.
func TestResolverVersionSource_NilReceiverReturnsNotOk(t *testing.T) {
	var src *resolverVersionSource
	got, ok := src.CurrentWorkspaceVersion(context.Background())
	if got != 0 || ok {
		t.Errorf("got (%d, %v), want (0, false)", got, ok)
	}
}

// TestResolverVersionSource_ResolverErrorReturnsNotOk: codex r2
// Medium #1 — pre-fix this returned bare 0, which the lazy provider
// treated as "version drift detected" and could evict a known-good
// cache. Post-fix returns ok=false so the cache is preserved.
func TestResolverVersionSource_ResolverErrorReturnsNotOk(t *testing.T) {
	r := &fakeResolver{err: errors.New("backend down")}
	r.version.Store(99) // ignored on err
	src := &resolverVersionSource{r: r}
	got, ok := src.CurrentWorkspaceVersion(context.Background())
	if got != 0 || ok {
		t.Errorf("got (%d, %v), want (0, false) (resolver err)", got, ok)
	}
}

// ---- bootProbeAndWarn -------------------------------------------------------

// bootCaller is a minimal AgentSynthesizer-shaped fake for the
// boot-probe wiring test. We don't import qa's internal fake — that
// would require exporting it; instead this lives next to the wiring
// it tests.
type bootCaller struct {
	available bool
	err       error
	caps      *llmcall.CapabilitiesResponse
	mu        sync.Mutex
}

func (b *bootCaller) IsAvailable() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.available
}

func (b *bootCaller) GetProviderCapabilities(_ context.Context, _, _ string) (*llmcall.CapabilitiesResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return nil, b.err
	}
	return b.caps, nil
}

func (b *bootCaller) AnswerQuestionWithTools(_ context.Context, _, _ string, _ *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error) {
	return &reasoningv1.AnswerQuestionWithToolsResponse{CapabilitySupported: true}, nil
}

// TestBootProbeAndWarn_HappyPath: probe succeeds, no stderr warning.
func TestBootProbeAndWarn_HappyPath(t *testing.T) {
	caller := &bootCaller{
		available: true,
		caps: &llmcall.CapabilitiesResponse{
			Resp: &reasoningv1.GetProviderCapabilitiesResponse{
				ToolUseSupported: true,
				Provider:         "test",
				Model:            "test",
			},
			Version: 1,
		},
	}
	lazy := qa.NewLazyAgentSynth(caller, nil, qa.LazyAgentSynthOptions{
		Timeout: 100 * time.Millisecond,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	stderr := captureStderr(t, func() {
		bootProbeAndWarn(lazy, "localhost:50051")
	})
	if strings.Contains(stderr, "warning:") {
		t.Errorf("happy path should not print warning to stderr; got: %s", stderr)
	}
	if lazy.LastProbeWasFailure() {
		t.Errorf("LastProbeWasFailure should be false on happy path")
	}
}

// TestBootProbeAndWarn_FailureWritesWarning: probe fails (worker
// unreachable), the boot helper writes the canonical warning naming
// the worker address AND the `make dev-worker` entry point. This is
// the regression guard for the tester's actual experience: a warning
// they would have noticed.
func TestBootProbeAndWarn_FailureWritesWarning(t *testing.T) {
	caller := &bootCaller{
		available: true,
		err:       errors.New("connection refused"),
	}
	lazy := qa.NewLazyAgentSynth(caller, nil, qa.LazyAgentSynthOptions{
		Timeout: 100 * time.Millisecond,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	stderr := captureStderr(t, func() {
		bootProbeAndWarn(lazy, "localhost:50051")
	})
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("failure path should print warning to stderr; got: %s", stderr)
	}
	if !strings.Contains(stderr, "localhost:50051") {
		t.Errorf("warning should name the worker address; got: %s", stderr)
	}
	if !strings.Contains(stderr, "make dev-worker") {
		t.Errorf("warning should suggest `make dev-worker`; got: %s", stderr)
	}
	if !lazy.LastProbeWasFailure() {
		t.Errorf("LastProbeWasFailure should be true after failed probe")
	}
}

// TestBootProbeAndWarn_NilLazy is a tolerance test — if for any reason
// the lazy provider isn't constructed (today: only when llmCaller is
// nil, but defensive programming wins), the helper must no-op rather
// than panic.
func TestBootProbeAndWarn_NilLazy(t *testing.T) {
	stderr := captureStderr(t, func() {
		bootProbeAndWarn(nil, "localhost:50051")
	})
	if stderr != "" {
		t.Errorf("nil-lazy should be a no-op; got stderr: %s", stderr)
	}
}

// TestBootProbeAndWarn_WorkerUnavailableSilent: when the caller
// reports IsAvailable=false at boot (e.g. local-mode without a worker
// configured at all), the lazy provider returns false without
// probing → no warning, because there's nothing to warn about.
//
// This is distinct from "worker configured but unreachable" (the
// failure-writes-warning case above).
func TestBootProbeAndWarn_WorkerUnavailableSilent(t *testing.T) {
	caller := &bootCaller{available: false}
	lazy := qa.NewLazyAgentSynth(caller, nil, qa.LazyAgentSynthOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	stderr := captureStderr(t, func() {
		bootProbeAndWarn(lazy, "localhost:50051")
	})
	if strings.Contains(stderr, "warning:") {
		t.Errorf("worker-unavailable should be silent (lazy provider returned false without probing); got: %s", stderr)
	}
}

// captureStderr redirects os.Stderr for the duration of fn and
// returns whatever was written. Restored on cleanup.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()
	_ = w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}
