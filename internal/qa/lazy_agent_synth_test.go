// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"

	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// fakeLazyCaller is the test seam — implements agentWorkerCaller for
// LazyAgentSynth tests. Tracks call counts so we can assert single-
// flight + caching behavior.
type fakeLazyCaller struct {
	mu               sync.Mutex
	available        bool
	probesCount      atomic.Int64
	answerCount      atomic.Int64
	probeErr         error
	probeReturn      *llmcall.CapabilitiesResponse
	probeBlock       chan struct{} // when non-nil, probe blocks until close
	answerErr        error
	probeStartedSync chan struct{} // signals "probe started, you can race now"
}

func (f *fakeLazyCaller) IsAvailable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *fakeLazyCaller) setAvailable(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.available = v
}

func (f *fakeLazyCaller) GetProviderCapabilities(ctx context.Context, _, _ string) (*llmcall.CapabilitiesResponse, error) {
	f.probesCount.Add(1)
	if f.probeStartedSync != nil {
		// Best-effort — non-blocking signal so tests can race the
		// second caller after the first one has entered the probe.
		select {
		case f.probeStartedSync <- struct{}{}:
		default:
		}
	}
	if f.probeBlock != nil {
		select {
		case <-f.probeBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return f.probeReturn, nil
}

func (f *fakeLazyCaller) AnswerQuestionWithTools(ctx context.Context, _, _ string, _ *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error) {
	f.answerCount.Add(1)
	if f.answerErr != nil {
		return nil, f.answerErr
	}
	return &reasoningv1.AnswerQuestionWithToolsResponse{CapabilitySupported: true}, nil
}

// fakeVersionSource returns a controllable workspace version. Lets
// tests simulate a "workspace save bumped the version" scenario, plus
// a "no version info available right now" condition (codex r2
// Medium #1) — toggle haveInfo to false to assert the cache is
// preserved rather than evicted.
type fakeVersionSource struct {
	version  atomic.Uint64
	haveInfo atomic.Bool
}

func (f *fakeVersionSource) CurrentWorkspaceVersion(_ context.Context) (uint64, bool) {
	return f.version.Load(), f.haveInfo.Load()
}

func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newCapsResponse(toolUse bool, version uint64) *llmcall.CapabilitiesResponse {
	return &llmcall.CapabilitiesResponse{
		Resp: &reasoningv1.GetProviderCapabilitiesResponse{
			Provider:          "test-provider",
			Model:             "test-model",
			ToolUseSupported:  toolUse,
		},
		Version: version,
	}
}

// ---- behavior tests --------------------------------------------------------

// TestLazyAgentSynth_NoCallerNeverProbes is the "don't waste a probe
// when you already know the worker is unreachable" rule. Pre-fix this
// invariant didn't exist and a spinning client would burn probe budget.
func TestLazyAgentSynth_NoCallerNeverProbes(t *testing.T) {
	caller := &fakeLazyCaller{available: false}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})

	for i := 0; i < 5; i++ {
		if lazy.SupportsTools(context.Background()) {
			t.Errorf("SupportsTools should be false when worker is unavailable")
		}
	}
	if got := caller.probesCount.Load(); got != 0 {
		t.Errorf("expected 0 probes when worker unavailable; got %d", got)
	}
}

// TestLazyAgentSynth_ColdStart_WorkerComesUpLater is the tester's
// scenario: API server boots first; worker starts a few seconds
// later; user asks a question. Pre-CA-126 agentic stayed disabled
// for the pod's lifetime. Now: first call after worker is up
// activates agentic.
func TestLazyAgentSynth_ColdStart_WorkerComesUpLater(t *testing.T) {
	caller := &fakeLazyCaller{
		available:   false, // worker not up yet
		probeReturn: newCapsResponse(true, 1),
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})

	// Stage 1: first call before worker is up → false, no probe.
	if lazy.SupportsTools(context.Background()) {
		t.Fatal("SupportsTools should be false before worker is reachable")
	}
	if caller.probesCount.Load() != 0 {
		t.Fatalf("no probe should run when worker is unreachable; got %d", caller.probesCount.Load())
	}

	// Stage 2: worker comes up.
	caller.setAvailable(true)

	// Stage 3: next call probes synchronously, succeeds.
	if !lazy.SupportsTools(context.Background()) {
		t.Fatal("SupportsTools should be true after worker is up and probe succeeded")
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 probe; got %d", got)
	}

	// Stage 4: subsequent calls hit the cache.
	for i := 0; i < 5; i++ {
		if !lazy.SupportsTools(context.Background()) {
			t.Errorf("SupportsTools should stay true on cached calls")
		}
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Errorf("subsequent calls should NOT re-probe; got %d total probes", got)
	}
}

// TestLazyAgentSynth_WarmStart confirms the rolling-restart case: API
// server + worker come up together, first traffic finds the worker
// reachable, probe succeeds, agentic activates immediately.
func TestLazyAgentSynth_WarmStart(t *testing.T) {
	caller := &fakeLazyCaller{
		available:   true,
		probeReturn: newCapsResponse(true, 1),
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})
	if !lazy.SupportsTools(context.Background()) {
		t.Fatal("warm start should activate agentic on first call")
	}
	if caller.probesCount.Load() != 1 {
		t.Errorf("expected exactly 1 probe; got %d", caller.probesCount.Load())
	}
}

// TestLazyAgentSynth_ProbeFailure_CooldownThenRetry: first probe
// fails; subsequent calls inside cooldown DON'T re-probe; calls after
// cooldown DO.
func TestLazyAgentSynth_ProbeFailure_CooldownThenRetry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &mockClock{now: now}

	caller := &fakeLazyCaller{
		available: true,
		probeErr:  errors.New("connection refused"),
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Cooldown: 60 * time.Second,
		Clock:    clock.Now,
		Logger:   newSilentLogger(),
	})

	// First call probes and fails.
	if lazy.SupportsTools(context.Background()) {
		t.Fatal("SupportsTools should be false after failed probe")
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Fatalf("expected 1 probe; got %d", got)
	}

	// Inside cooldown — no re-probe.
	clock.advance(30 * time.Second)
	if lazy.SupportsTools(context.Background()) {
		t.Error("should still be false")
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Errorf("expected NO re-probe inside cooldown; got %d total", got)
	}

	// Past cooldown — re-probe runs.
	clock.advance(31 * time.Second)
	// Make the second probe succeed.
	caller.probeErr = nil
	caller.probeReturn = newCapsResponse(true, 1)

	if !lazy.SupportsTools(context.Background()) {
		t.Fatal("re-probe after cooldown should succeed")
	}
	if got := caller.probesCount.Load(); got != 2 {
		t.Errorf("expected 2 probes total (initial + retry); got %d", got)
	}
}

// TestLazyAgentSynth_ProviderDoesNotSupportTools pins the "hard no"
// case: provider exists but doesn't support tools. Cache success with
// toolsEnabled=false; subsequent calls return false; NO re-probe (it's
// not a transient failure).
func TestLazyAgentSynth_ProviderDoesNotSupportTools(t *testing.T) {
	caller := &fakeLazyCaller{
		available:   true,
		probeReturn: newCapsResponse(false, 1), // toolUse=false
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})

	for i := 0; i < 5; i++ {
		if lazy.SupportsTools(context.Background()) {
			t.Errorf("SupportsTools should be false when provider doesn't support tools")
		}
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 probe; got %d", got)
	}
}

// TestLazyAgentSynth_VersionDriftRereprobes simulates a workspace LLM
// config save — the resolver's snapshot version bumps; the next
// SupportsTools call detects the drift and re-probes.
func TestLazyAgentSynth_VersionDriftRereprobes(t *testing.T) {
	caller := &fakeLazyCaller{
		available:   true,
		probeReturn: newCapsResponse(true, 1),
	}
	versionSrc := &fakeVersionSource{}
	versionSrc.version.Store(1)
	versionSrc.haveInfo.Store(true)

	lazy := NewLazyAgentSynth(caller, versionSrc, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})

	// First call probes, caches version=1.
	_ = lazy.SupportsTools(context.Background())
	if got := caller.probesCount.Load(); got != 1 {
		t.Fatalf("expected 1 initial probe; got %d", got)
	}

	// Subsequent calls don't re-probe (version unchanged).
	for i := 0; i < 3; i++ {
		_ = lazy.SupportsTools(context.Background())
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Fatalf("no re-probe expected when version unchanged; got %d", got)
	}

	// Workspace save bumps the version to 2; update the probe response
	// so the new probe also returns version=2 (consistent with what
	// the worker would see).
	versionSrc.version.Store(2)
	caller.probeReturn = newCapsResponse(true, 2)

	// Next call detects drift, re-probes.
	_ = lazy.SupportsTools(context.Background())
	if got := caller.probesCount.Load(); got != 2 {
		t.Errorf("expected re-probe on version drift; got %d total", got)
	}

	// Subsequent calls don't re-probe again until next drift.
	for i := 0; i < 3; i++ {
		_ = lazy.SupportsTools(context.Background())
	}
	if got := caller.probesCount.Load(); got != 2 {
		t.Errorf("no further re-probe expected after caching new version; got %d", got)
	}
}

// TestLazyAgentSynth_SingleFlight pins that a burst of concurrent
// SupportsTools calls fire EXACTLY ONE underlying probe. Without
// single-flight a 100-request first-traffic burst would smash the
// worker.
func TestLazyAgentSynth_SingleFlight(t *testing.T) {
	probeBlock := make(chan struct{})
	probeStarted := make(chan struct{}, 1)
	caller := &fakeLazyCaller{
		available:        true,
		probeReturn:      newCapsResponse(true, 1),
		probeBlock:       probeBlock,
		probeStartedSync: probeStarted,
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Timeout: 5 * time.Second,
		Logger:  newSilentLogger(),
	})

	const N = 50
	var wg sync.WaitGroup
	results := make([]bool, N)

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = lazy.SupportsTools(context.Background())
		}(i)
	}

	// Wait until at least one goroutine has entered the probe RPC, then
	// release. Subsequent goroutines must coalesce on the in-flight.
	<-probeStarted
	// Brief sleep to ensure other goroutines have queued behind the
	// in-flight before we release. Race: if the probe finishes before
	// all goroutines arrive, late ones get the cached result and don't
	// trigger more probes — that's also fine; the assertion is "exactly
	// one underlying probe."
	time.Sleep(10 * time.Millisecond)
	close(probeBlock)

	wg.Wait()

	if got := caller.probesCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 probe under single-flight; got %d", got)
	}
	for i, r := range results {
		if !r {
			t.Errorf("result[%d] = false; expected all true", i)
		}
	}
}

// TestLazyAgentSynth_ProbeTimeout pins the deadline-bound probe: if
// the worker hangs longer than Timeout, the probe is cancelled and
// the cache records a failure with cooldown set.
func TestLazyAgentSynth_ProbeTimeout(t *testing.T) {
	probeBlock := make(chan struct{})
	defer close(probeBlock) // unblock at end of test
	caller := &fakeLazyCaller{
		available:  true,
		probeBlock: probeBlock,
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Timeout: 50 * time.Millisecond,
		Logger:  newSilentLogger(),
	})

	start := time.Now()
	if lazy.SupportsTools(context.Background()) {
		t.Error("SupportsTools should be false on probe timeout")
	}
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("probe took too long; expected ~50ms, got %v", elapsed)
	}
	if !lazy.LastProbeWasFailure() {
		t.Error("expected LastProbeWasFailure=true after timeout")
	}
	if lazy.LastProbeError() == nil {
		t.Error("expected LastProbeError != nil after timeout")
	}
}

// TestLazyAgentSynth_ResolverNil tolerates a nil resolver — embedded
// mode without a workspace store. The cache treats itself as
// non-stale (probedVer == 0 == resolver's "no info" return).
func TestLazyAgentSynth_ResolverNil(t *testing.T) {
	caller := &fakeLazyCaller{
		available:   true,
		probeReturn: newCapsResponse(true, 0), // version 0 — embedded mode
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})
	for i := 0; i < 3; i++ {
		if !lazy.SupportsTools(context.Background()) {
			t.Errorf("SupportsTools should be true with nil resolver and successful probe")
		}
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 probe with nil resolver; got %d", got)
	}
}

// TestLazyAgentSynth_AnswerQuestionWithTools_DelegatesOnSuccess
// confirms the AnswerQuestionWithTools path: probe → success → answer
// is delegated to the underlying WorkerAgentSynthesizer.
func TestLazyAgentSynth_AnswerQuestionWithTools_DelegatesOnSuccess(t *testing.T) {
	caller := &fakeLazyCaller{
		available:   true,
		probeReturn: newCapsResponse(true, 1),
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})

	_, err := lazy.AnswerQuestionWithTools(context.Background(), AgentTurnRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("AnswerQuestionWithTools: %v", err)
	}
	if got := caller.answerCount.Load(); got != 1 {
		t.Errorf("expected 1 underlying Answer call; got %d", got)
	}
}

// TestLazyAgentSynth_AnswerQuestionWithTools_RefusesOnDecline pins
// the error shape on the "lazy probe says no" path so the
// orchestrator's existing fallback continues to work.
func TestLazyAgentSynth_AnswerQuestionWithTools_RefusesOnDecline(t *testing.T) {
	caller := &fakeLazyCaller{
		available: true,
		probeErr:  errors.New("connection refused"),
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})

	_, err := lazy.AnswerQuestionWithTools(context.Background(), AgentTurnRequest{})
	if err == nil {
		t.Fatal("expected error when lazy probe declines")
	}
	if got := caller.answerCount.Load(); got != 0 {
		t.Errorf("AnswerQuestionWithTools must NOT delegate when SupportsTools=false; got %d underlying calls", got)
	}
}

// ---- codex r2 regression tests -------------------------------------------

// TestLazyAgentSynth_WarmUpFailure_DoesNotPoisonRequestCache is the
// codex r2 High finding's regression guard. Pre-fix:
//   1. WarmUp ran the same SupportsTools path as real requests.
//   2. Boot probe failed (worker not up yet) → state=failure,
//      nextRetry=now+60s.
//   3. User starts the worker 5s later, asks a question.
//   4. SupportsTools returns false (cooldown not elapsed) → agentic
//      stays disabled for the rest of the minute. Defeats the whole
//      "lazy probe recovers" promise.
//
// Post-fix: WarmUp records lastErr (so the boot warning still fires)
// but does NOT install a cooldown. The first real request after the
// worker comes up probes fresh.
func TestLazyAgentSynth_WarmUpFailure_DoesNotPoisonRequestCache(t *testing.T) {
	caller := &fakeLazyCaller{
		available: true,
		probeErr:  errors.New("connection refused"),
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &mockClock{now: now}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Cooldown: 60 * time.Second,
		Timeout:  100 * time.Millisecond,
		Clock:    clock.Now,
		Logger:   newSilentLogger(),
	})

	// Stage 1: WarmUp at boot — fails because worker is "down."
	if lazy.WarmUp(context.Background()) {
		t.Fatal("WarmUp should return false when probe fails")
	}
	if !lazy.LastProbeWasFailure() {
		t.Fatal("LastProbeWasFailure should be true so the boot warning fires")
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Fatalf("expected 1 boot probe; got %d", got)
	}

	// Stage 2: worker comes up; probe will now succeed.
	caller.probeErr = nil
	caller.probeReturn = newCapsResponse(true, 1)

	// Stage 3: real request, immediately (well within the would-be
	// 60s cooldown). Pre-fix this returned false; post-fix it probes
	// fresh and succeeds.
	if !lazy.SupportsTools(context.Background()) {
		t.Fatal("SupportsTools should probe fresh after a boot warm-up failure (no cooldown installed)")
	}
	if got := caller.probesCount.Load(); got != 2 {
		t.Errorf("expected exactly 2 probes (warm-up + first request); got %d", got)
	}
}

// TestLazyAgentSynth_VersionSourceNoInfo_KeepsCache is the codex r2
// Medium #1 regression guard. A non-nil resolver that returns ok=false
// (e.g. transient backend error) must NOT cause the lazy provider to
// re-probe — the cache should stay as-is.
func TestLazyAgentSynth_VersionSourceNoInfo_KeepsCache(t *testing.T) {
	caller := &fakeLazyCaller{
		available:   true,
		probeReturn: newCapsResponse(true, 1),
	}
	versionSrc := &fakeVersionSource{}
	versionSrc.version.Store(1)
	versionSrc.haveInfo.Store(true)

	lazy := NewLazyAgentSynth(caller, versionSrc, LazyAgentSynthOptions{
		Logger: newSilentLogger(),
	})

	// Initial probe → cache success at version 1.
	if !lazy.SupportsTools(context.Background()) {
		t.Fatal("initial probe should succeed")
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Fatalf("expected 1 initial probe; got %d", got)
	}

	// Resolver hiccups: temporarily can't tell us a version.
	versionSrc.haveInfo.Store(false)

	// Subsequent calls must NOT re-probe — cache stays usable.
	for i := 0; i < 5; i++ {
		if !lazy.SupportsTools(context.Background()) {
			t.Errorf("SupportsTools should keep cached success when version source returns ok=false")
		}
	}
	if got := caller.probesCount.Load(); got != 1 {
		t.Errorf("expected NO re-probe when version source can't tell us a version; got %d total", got)
	}

	// Resolver recovers and reports a different version → re-probe.
	versionSrc.haveInfo.Store(true)
	versionSrc.version.Store(2)
	caller.probeReturn = newCapsResponse(true, 2)

	if !lazy.SupportsTools(context.Background()) {
		t.Fatal("expected re-probe + success after resolver recovers with new version")
	}
	if got := caller.probesCount.Load(); got != 2 {
		t.Errorf("expected re-probe on real version drift; got %d total", got)
	}
}

// TestLazyAgentSynth_CtxCancelDoesNotCommitFailure is the codex r2
// Medium #2 regression guard. If a request context is canceled while
// it owns the cold probe, the request returns false (its own caller
// is gone) — but the shared cache state must NOT be set to failure,
// otherwise other in-flight requests get suppressed for the cooldown
// duration based on one client's cancellation.
func TestLazyAgentSynth_CtxCancelDoesNotCommitFailure(t *testing.T) {
	probeBlock := make(chan struct{})
	caller := &fakeLazyCaller{
		available:  true,
		probeBlock: probeBlock,
	}
	lazy := NewLazyAgentSynth(caller, nil, LazyAgentSynthOptions{
		Cooldown: 60 * time.Second,
		Timeout:  5 * time.Second,
		Logger:   newSilentLogger(),
	})

	// Cancel the request context partway through the probe.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
		// Then unblock the probe so it returns ctx.Err().
		close(probeBlock)
	}()
	if lazy.SupportsTools(ctx) {
		t.Fatal("SupportsTools should return false when request ctx is canceled")
	}

	// Now make a fresh request. The cache must not have been poisoned
	// by the canceled call: a happy-path probe should run (state was
	// not committed to failure).
	caller.probeBlock = nil // clear so the probe completes immediately
	caller.probeReturn = newCapsResponse(true, 1)
	if !lazy.SupportsTools(context.Background()) {
		t.Errorf("expected fresh probe to succeed; canceled-ctx probe must not have committed failure state")
	}
	// We expect: probe #1 (canceled), probe #2 (fresh) — exactly 2.
	if got := caller.probesCount.Load(); got != 2 {
		t.Errorf("expected 2 probes (canceled + fresh); got %d", got)
	}
}

// ---- helpers ---------------------------------------------------------------

type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mockClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
