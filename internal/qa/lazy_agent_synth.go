// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// LazyAgentSynth is the production AgentSynthesizer the orchestrator
// wraps around the worker. It defers the capability probe until the
// first agentic-eligible request, caches the verdict, and re-probes
// after a cooldown on failure or whenever the resolver's workspace
// version drifts (a workspace LLM-config save bumps the version).
//
// Replaces the one-shot startup-probe story at internal/api/rest/router.go
// pre-CA-126: previously, if the worker wasn't reachable in the first
// ~30s of API server boot, the synthesizer was never wired and agentic
// features stayed disabled for the pod's entire lifetime. Tester
// report 2026-04-30 (Pazaryna) Issue 2.
//
// Concurrency model: a single mutex guards all cached state. Concurrent
// SupportsTools calls that find no usable cache entry coalesce on a
// single in-flight probe (single-flight) so a sudden burst of first-
// request traffic still fires exactly one underlying RPC.
// hardCapacityCeiling is the maximum effective value returned by
// UpstreamCapacity. Defense-in-depth clamp matching D9 / H1.
// The DB CHECK constraint and WorkerConfig validator are the primary
// guards; this Go-side clamp prevents a future schema change from
// causing goroutine exhaustion.
const hardCapacityCeiling = 256

type LazyAgentSynth struct {
	caller   agentWorkerCaller
	resolver ProbeVersionSource
	cooldown time.Duration
	timeout  time.Duration
	clock    func() time.Time
	log      *slog.Logger

	mu        sync.Mutex
	state     probeState
	syn       *WorkerAgentSynthesizer // valid only when state == probeStateSuccess
	probedVer uint64                  // resolver Snapshot.Version at probe time
	nextRetry time.Time               // valid only when state == probeStateFailure
	inflight  *probeInFlight          // single-flight handle; nil when no probe is running
	lastErr   error                   // most recent probe error (diagnostic only)

	// Capacity cache (M2): stored independently of syn so that probe
	// failures after a successful first probe preserve the last-known-good
	// capacity value (fail-open). Only a successful probe updates these.
	capacityValue int  // last successfully probed max_concurrent_calls value
	capacityKnown bool // last successfully probed max_concurrent_calls_known flag
}

// ProbeVersionSource is the narrow surface LazyAgentSynth needs from
// the resolver: just enough to ask "what's the current workspace
// version?" so the cache can stale-check after a workspace save.
//
// The (uint64, bool) return shape disambiguates the two real cases:
//
//   - (v, true)  → "the current workspace version is v." A cached
//     probe whose probedVer != v is stale; re-probe.
//   - (_, false) → "I cannot tell you a current version right now"
//     (resolver unavailable, embedded mode without a workspace store,
//     transient backend error). LazyAgentSynth treats this as
//     "no fresher information" and keeps the cache as-is — never
//     evicts a known-good capability cache because of resolver flakiness.
//
// Codex r2 finding (Medium): pre-fix, returning a bare 0 conflated
// "real version 0" with "no info," and the caller treated 0 as
// "stale" in the version-drift branch. That could evict a healthy
// success cache after a probe at version > 0 and a follow-up where
// the resolver briefly returned 0 (errored out, was nil, etc.).
type ProbeVersionSource interface {
	CurrentWorkspaceVersion(ctx context.Context) (version uint64, ok bool)
}

// LazyAgentSynthOptions configure non-default knobs.
type LazyAgentSynthOptions struct {
	// Cooldown is how long to wait after a failed probe before
	// re-probing. Default: 60s. Shorter = more probe traffic when
	// the worker is genuinely down; longer = users wait longer for
	// agentic to activate after the worker comes up.
	Cooldown time.Duration

	// Timeout caps each individual probe RPC. Default: 2s. The first
	// agentic request after a cold start blocks on this duration when
	// the worker is unreachable, so don't make it too long. Production
	// users with a slow worker should increase this; local-dev users
	// rarely notice 2s.
	Timeout time.Duration

	// Clock is the time source. Defaults to time.Now. Tests inject a
	// controllable clock to test cooldown expiry without sleeping.
	Clock func() time.Time

	// Logger is the destination for probe-success / probe-failure
	// log lines. Defaults to slog.Default.
	Logger *slog.Logger
}

type probeState int

const (
	probeStateUnprobed probeState = iota
	probeStateSuccess
	probeStateFailure
)

// probeInFlight is the single-flight handle. Only one probe can run at
// a time per LazyAgentSynth; subsequent SupportsTools callers wait on
// `done` to be closed and then re-read the cache.
type probeInFlight struct {
	done chan struct{}
}

// NewLazyAgentSynth constructs a new lazy provider. caller is the
// gRPC worker client; resolver supplies the workspace version (may be
// nil for embedded/no-DB mode); opts override defaults.
func NewLazyAgentSynth(caller agentWorkerCaller, resolver ProbeVersionSource, opts LazyAgentSynthOptions) *LazyAgentSynth {
	if opts.Cooldown == 0 {
		opts.Cooldown = 60 * time.Second
	}
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Second
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &LazyAgentSynth{
		caller:   caller,
		resolver: resolver,
		cooldown: opts.Cooldown,
		timeout:  opts.Timeout,
		clock:    opts.Clock,
		log:      opts.Logger,
	}
}

// supportsToolsMode controls how SupportsTools handles probe failures.
// callerOnRequest = real user traffic; failure → commit cache state.
// callerOnBoot = best-effort warm-up; failure → diagnostic-only,
// don't commit a cooldown that would suppress real requests.
type supportsToolsMode int

const (
	modeRequest supportsToolsMode = iota
	modeBootWarmup
)

// SupportsTools implements AgentSynthesizer. Decision tree (per
// CA-126 plan):
//
//  1. Worker not available → false (don't waste a probe).
//  2. Cached success && version unchanged (or no fresh info) → toolsEnabled (cheap).
//  3. Cached success && version drifted → re-probe.
//  4. Cached failure && cooldown not elapsed → false.
//  5. Cached failure && cooldown elapsed → re-probe.
//  6. Unprobed → probe.
//
// Single-flight: concurrent calls reaching step 3/5/6 share one
// underlying probe RPC.
//
// Codex r2 fixes baked into this implementation:
//   - resolver I/O happens OUTSIDE the synth mutex (Medium #3 — the
//     mutex protects only fast in-memory reads; the resolver call
//     happens after we release and we re-acquire to revalidate).
//   - "no version info" (ok=false from resolver) is not treated as
//     drift; the cache stays as-is (Medium #1).
//   - request-context cancellation does not commit a cooldown into the
//     shared cache (Medium #2 — we check ctx.Err() before mutating
//     state in doProbe).
func (l *LazyAgentSynth) SupportsTools(ctx context.Context) bool {
	return l.supportsToolsMode(ctx, modeRequest)
}

// WarmUp is the boot-side variant of SupportsTools. Caches success
// normally; on failure records the diagnostic state
// (LastProbeWasFailure / LastProbeError stay accurate so the boot
// warning goroutine can fire) but does NOT commit a long-lived
// cooldown. The first real request after the worker comes up probes
// fresh and activates immediately — preserves the local-dev recovery
// story even when the boot probe fired before the worker was ready.
//
// Codex r2 finding (High): pre-fix, the boot probe shared the same
// 60s cooldown as request probes, so a `make dev` user who started
// the worker 5s after `make dev` would still wait the full minute
// before agentic activated.
func (l *LazyAgentSynth) WarmUp(ctx context.Context) bool {
	return l.supportsToolsMode(ctx, modeBootWarmup)
}

func (l *LazyAgentSynth) supportsToolsMode(ctx context.Context, mode supportsToolsMode) bool {
	if l == nil {
		return false
	}
	if l.caller == nil || !l.caller.IsAvailable() {
		return false
	}

	// Phase 1: snapshot cached state under the mutex. We then RELEASE
	// the mutex before doing any I/O (resolver call or probe RPC).
	l.mu.Lock()
	state := l.state
	probedVer := l.probedVer
	syn := l.syn
	nextRetry := l.nextRetry
	l.mu.Unlock()

	// Phase 2: based on cached state, decide whether we need a probe.
	// The resolver call runs OUTSIDE the lock (Medium #3 fix).
	switch state {
	case probeStateSuccess:
		// Ask the resolver for the current version. If the resolver
		// can't tell us right now (ok=false), trust the cache.
		curVer, ok := l.currentWorkspaceVersionUnlocked(ctx)
		if !ok || curVer == probedVer {
			return syn != nil && syn.toolsEnabled
		}
		// Version drifted — fall through to re-probe.
	case probeStateFailure:
		if l.clock().Before(nextRetry) {
			return false
		}
		// Cooldown elapsed — fall through to re-probe.
	case probeStateUnprobed:
		// Fall through to probe.
	}

	// Phase 3: probe needed. Re-acquire the mutex to either join the
	// in-flight probe (single-flight) or start a new one. We re-read
	// l.inflight here because another goroutine may have started one
	// while we were doing resolver I/O outside the lock.
	l.mu.Lock()

	if l.inflight != nil {
		done := l.inflight.done
		l.mu.Unlock()
		select {
		case <-done:
			// Probe finished; re-evaluate fresh state. Tail-call
			// recursion is bounded by deterministic transitions.
			return l.supportsToolsMode(ctx, mode)
		case <-ctx.Done():
			return false
		}
	}

	// We are the goroutine that runs this probe.
	l.inflight = &probeInFlight{done: make(chan struct{})}
	inflight := l.inflight
	l.mu.Unlock()

	probeErr := l.doProbe(ctx, mode)

	// Probe done — close the inflight and clear it. doProbe has
	// already updated l.state/l.syn/l.nextRetry under the mutex
	// (or skipped the state update if mode was boot or ctx was done).
	l.mu.Lock()
	close(inflight.done)
	l.inflight = nil
	finalState := l.state
	finalSyn := l.syn
	l.mu.Unlock()

	if probeErr != nil {
		return false
	}
	if finalState == probeStateSuccess && finalSyn != nil {
		return finalSyn.toolsEnabled
	}
	return false
}

// AnswerQuestionWithTools implements AgentSynthesizer. Ensures the
// cache is hot via SupportsTools first; refuses with the same error
// shape as today's WorkerAgentSynthesizer when the cache says
// "no tools" (so the orchestrator's existing fallback path is unchanged).
func (l *LazyAgentSynth) AnswerQuestionWithTools(ctx context.Context, req AgentTurnRequest) (AgentTurn, error) {
	if !l.SupportsTools(ctx) {
		return AgentTurn{}, fmt.Errorf("agent synth: capability_supported=false (lazy probe declined)")
	}
	l.mu.Lock()
	syn := l.syn
	l.mu.Unlock()
	if syn == nil {
		return AgentTurn{}, fmt.Errorf("agent synth: lazy probe inconsistency (state=success but syn is nil)")
	}
	return syn.AnswerQuestionWithTools(ctx, req)
}

// LastProbeWasFailure exposes the failure state for the boot-warning
// goroutine. Returns true when the most recent probe attempt failed
// — including the boot warm-up path where state remains
// probeStateUnprobed (so the next real request still re-probes
// freshly) but lastErr was set. Returns false when no probe has run
// or the probe succeeded.
func (l *LazyAgentSynth) LastProbeWasFailure() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == probeStateFailure {
		return true
	}
	// Boot warm-up failure: state stays Unprobed (so real requests
	// re-probe fresh) but lastErr captures the failure.
	if l.state == probeStateUnprobed && l.lastErr != nil {
		return true
	}
	return false
}

// LastProbeError returns the most recent probe error for diagnostic
// surfacing (e.g. the boot-warning stderr line). Returns nil when no
// probe has failed.
func (l *LazyAgentSynth) LastProbeError() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
}

// UpstreamCapacity returns the LLM backend's declared parallel inference
// capacity. It shares the same underlying probe RPC as SupportsTools (M4
// single-flight), so calling UpstreamCapacity never fires a second gRPC RPC
// when a SupportsTools probe is already in-flight or completed.
//
// Return semantics (D3 encoding / D5 failure handling):
//   - (N, true, nil)   → clamp orchestrator to N (1 ≤ N ≤ 256)
//   - (0, true, nil)   → unbounded (frontier API); orchestrator does not clamp
//   - (0, false, nil)  → unknown; orchestrator does not clamp (fail-open)
//   - (0, false, err)  → first probe failed; no last-known-good; fail-open
//   - (N, true, nil)   → returned after a re-probe failure when a prior
//     success cached N (last-known-good, M2)
//
// Capacity probe failure does NOT install the 60s agentic cooldown (M5).
// This is a separate concern — a flaky worker should not disable agentic
// synthesis just because the capacity field is temporarily unavailable.
//
// No repoID parameter (H2): the runner pre-resolves the profile and passes
// a profile-bound closure as the UpstreamCapacityProvider.
func (l *LazyAgentSynth) UpstreamCapacity(ctx context.Context) (int, bool, error) {
	if l == nil || l.caller == nil || !l.caller.IsAvailable() {
		return 0, false, nil
	}

	// Phase 1: read cached capacity under the mutex.
	l.mu.Lock()
	state := l.state
	cachedVal := l.capacityValue
	cachedKnown := l.capacityKnown
	inflight := l.inflight
	l.mu.Unlock()

	// If we already have a successful probe, return the cached capacity.
	if state == probeStateSuccess {
		return cachedVal, cachedKnown, nil
	}

	// If a probe is already in-flight (started by SupportsTools or a
	// concurrent UpstreamCapacity call), join it (M4 single-flight).
	if inflight != nil {
		select {
		case <-inflight.done:
			l.mu.Lock()
			val := l.capacityValue
			known := l.capacityKnown
			newState := l.state
			l.mu.Unlock()
			if newState == probeStateSuccess {
				return val, known, nil
			}
			// Probe finished but failed; return last-known-good or unknown.
			return val, known, nil
		case <-ctx.Done():
			// Context canceled while waiting. Return last-known-good (may be zero).
			l.mu.Lock()
			val := l.capacityValue
			known := l.capacityKnown
			l.mu.Unlock()
			return val, known, ctx.Err()
		}
	}

	// Phase 2: no probe in flight and state is not success. Fire a probe.
	// NOTE: capacity probe failures do NOT install the 60s agentic cooldown
	// (M5). We use modeBootWarmup here because it leaves state=Unprobed on
	// failure — this means the next UpstreamCapacity call will retry without
	// waiting for a cooldown period.
	l.mu.Lock()
	// Re-check: another goroutine may have started a probe between our
	// unlock above and this re-lock.
	if l.inflight != nil {
		done := l.inflight.done
		l.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		l.mu.Lock()
		val := l.capacityValue
		known := l.capacityKnown
		l.mu.Unlock()
		return val, known, nil
	}
	if l.state == probeStateSuccess {
		val := l.capacityValue
		known := l.capacityKnown
		l.mu.Unlock()
		return val, known, nil
	}
	// Start the probe.
	l.inflight = &probeInFlight{done: make(chan struct{})}
	inflightHandle := l.inflight
	l.mu.Unlock()

	probeErr := l.doProbe(ctx, modeBootWarmup)

	// Done. Notify waiters and clear inflight.
	l.mu.Lock()
	close(inflightHandle.done)
	l.inflight = nil
	val := l.capacityValue
	known := l.capacityKnown
	l.mu.Unlock()

	return val, known, probeErr
}

// doProbe runs a single probe RPC and updates the cache. Caller must
// not hold l.mu (we acquire it briefly to write results). Single-
// flight is enforced by SupportsTools setting l.inflight before
// calling here.
//
// mode controls whether failures commit to the request-cache state
// (modeRequest) or are recorded as diagnostic-only (modeBootWarmup —
// success still cached so warm starts are fast, but failure does not
// install a 60s cooldown that would suppress real requests).
//
// Returns the probe error (nil on success) so the caller knows when
// to short-circuit.
func (l *LazyAgentSynth) doProbe(ctx context.Context, mode supportsToolsMode) error {
	probeCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()

	capsWrap, err := l.caller.GetProviderCapabilities(probeCtx, "", resolution.OpProviderCapabilities)

	// Codex r2 finding (Medium #2): if the parent request context
	// was already canceled (caller hung up before / during the RPC),
	// don't commit cache state. Otherwise a timed-out request can
	// install a process-wide 60s cooldown that suppresses agentic
	// for every other in-flight request. We surface the error to the
	// caller (which returns false to its own user) but leave the
	// shared cache untouched.
	if ctx.Err() != nil {
		return ctx.Err()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if err != nil {
		// Codex r2 finding (High): boot warm-up failures must NOT
		// commit a long-lived cooldown into the request cache. They
		// stay diagnostic-only so the next real request probes
		// fresh — preserves the local-dev "start worker after API"
		// recovery story.
		l.lastErr = err
		if mode == modeBootWarmup {
			l.log.Info("agent synth: boot warm-up probe failed; first real request will probe again",
				"err", err)
			return err
		}
		l.state = probeStateFailure
		l.nextRetry = l.clock().Add(l.cooldown)
		// keep l.syn nil so a stale success cache doesn't bleed through
		l.syn = nil
		l.log.Warn("agent synth: lazy probe failed; agentic disabled until cooldown elapses",
			"err", err, "next_retry_at", l.nextRetry)
		return err
	}
	if capsWrap == nil || capsWrap.Resp == nil {
		// Treat malformed response as a failure so we re-probe.
		nilErr := fmt.Errorf("nil capability response")
		l.lastErr = nilErr
		if mode == modeBootWarmup {
			l.log.Info("agent synth: boot warm-up probe returned nil response; first real request will probe again")
			return nilErr
		}
		l.state = probeStateFailure
		l.nextRetry = l.clock().Add(l.cooldown)
		l.syn = nil
		l.log.Warn("agent synth: lazy probe returned nil response",
			"next_retry_at", l.nextRetry)
		return nilErr
	}

	l.lastErr = nil

	// Cache capacity fields independently of tool-use (M2 — capacity
	// cache survives a probe failure; it is not cleared on error).
	// D9: clamp to hardCapacityCeiling as defense-in-depth.
	rawCap := int(capsWrap.Resp.GetMaxConcurrentCalls())
	rawKnown := capsWrap.Resp.GetMaxConcurrentCallsKnown()
	if rawCap < 0 {
		// Invalid value per D3 encoding table — treat as unknown.
		rawKnown = false
		rawCap = 0
		l.log.Warn("agent synth: GetProviderCapabilities returned negative max_concurrent_calls; treating as unknown",
			"raw_value", rawCap)
	}
	if rawCap > hardCapacityCeiling {
		l.log.Warn("agent synth: max_concurrent_calls exceeds hard ceiling; clamped",
			"reported", rawCap, "ceiling", hardCapacityCeiling)
		rawCap = hardCapacityCeiling
	}
	l.capacityValue = rawCap
	l.capacityKnown = rawKnown

	if !capsWrap.Resp.GetToolUseSupported() {
		// Provider exists but doesn't support tools. That's a hard
		// "no agentic for this provider," NOT a transient failure.
		// Cache success with toolsEnabled=false so we don't re-probe
		// every cooldown interval. The version-drift check still
		// catches a workspace save that switches providers — it'll
		// re-probe and may flip toolsEnabled back to true.
		l.state = probeStateSuccess
		l.syn = NewWorkerAgentSynthesizerWithVersion(l.caller, false, capsWrap.Version)
		l.probedVer = capsWrap.Version
		l.log.Info("agent synth: provider does not support tool use; agentic disabled (cached)",
			"provider", capsWrap.Resp.GetProvider(),
			"model", capsWrap.Resp.GetModel(),
			"probed_version", capsWrap.Version,
			"max_concurrent_calls", rawCap,
			"max_concurrent_calls_known", rawKnown)
		return nil
	}
	l.state = probeStateSuccess
	l.syn = NewWorkerAgentSynthesizerWithVersion(l.caller, true, capsWrap.Version)
	l.probedVer = capsWrap.Version
	l.log.Info("agent synth: lazy probe succeeded; agentic enabled",
		"provider", capsWrap.Resp.GetProvider(),
		"model", capsWrap.Resp.GetModel(),
		"probed_version", capsWrap.Version,
		"max_concurrent_calls", rawCap,
		"max_concurrent_calls_known", rawKnown)
	return nil
}

// currentWorkspaceVersionUnlocked queries the resolver for the current
// workspace snapshot version. Returns (version, true) when the
// resolver supplied a definite answer; (_, false) when no resolver is
// wired or the resolver couldn't tell us right now.
//
// Codex r2 finding (Medium #1): the (uint64, bool) shape lets the
// caller distinguish "real version 0" from "resolver had no info,"
// so resolver flakiness can't evict a known-good capability cache.
//
// MUST NOT be called with l.mu held — the resolver may do DB-backed
// work and serializing all SupportsTools callers behind that would
// stall agentic-gate decisions during a resolver hiccup
// (Codex r2 Medium #3).
func (l *LazyAgentSynth) currentWorkspaceVersionUnlocked(ctx context.Context) (uint64, bool) {
	if l.resolver == nil {
		return 0, false
	}
	return l.resolver.CurrentWorkspaceVersion(ctx)
}
