// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/qa"
)

// resolverVersionSource adapts resolution.Resolver to qa's narrower
// ProbeVersionSource interface. The lazy capability provider in
// internal/qa needs only the workspace snapshot version; this avoids
// dragging the whole resolver interface into the qa package.
//
// CA-126 / Wave 3.
type resolverVersionSource struct {
	r resolution.Resolver
}

// CurrentWorkspaceVersion implements qa.ProbeVersionSource. Returns
// (version, true) when the resolver supplied a definite answer;
// (_, false) when no resolver is wired (embedded mode without a
// workspace store), the receiver itself is nil, or the resolver
// returned an error.
//
// Codex r2 finding (Medium #1): the (uint64, bool) shape lets the
// lazy provider distinguish "real version 0" from "no info." A
// resolver hiccup that briefly returns false will NOT evict the
// capability cache, which previously could happen when the resolver
// returned a bare 0.
func (s *resolverVersionSource) CurrentWorkspaceVersion(ctx context.Context) (uint64, bool) {
	if s == nil || s.r == nil {
		return 0, false
	}
	snap, err := s.r.Resolve(ctx, "", resolution.OpProviderCapabilities)
	if err != nil {
		return 0, false
	}
	return snap.Version, true
}

// bootProbeAndWarn runs the best-effort boot probe in the background.
// Two responsibilities:
//
//  1. Hot-warm the cache so the K8s rolling-restart case (worker
//     usually up within seconds of the API) doesn't pay probe latency
//     on the first real agentic request.
//  2. If the probe failed, print a one-line stderr warning naming the
//     canonical entry point so a `make dev` user immediately knows
//     what to do. Does NOT install a long-lived cooldown — the boot
//     probe uses qa.LazyAgentSynth's WarmUp variant which is success-
//     caching but failure-diagnostic-only. The first real agentic
//     request after the worker comes up probes fresh and activates
//     immediately (codex r2 High finding fix).
//
// Does NOT block boot. The function returns when both steps finish;
// it's invoked in a goroutine.
//
// CA-126 / Wave 3 — replaces the pre-fix synchronous 30s
// probe-with-retry that left the synthesizer permanently nil on
// failure.
func bootProbeAndWarn(lazy *qa.LazyAgentSynth, workerAddr string) {
	if lazy == nil {
		return
	}
	// Step 1: hot-warm the cache via the warm-up variant. SupportsTools
	// internally times out per the lazy provider's configured Timeout;
	// we add a small outer budget here to bound the total wall-clock
	// just in case.
	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = lazy.WarmUp(probeCtx)

	// Step 2: if the probe failed, surface a friendly warning. We
	// check after the probe completes (rather than on a fixed
	// timer) so the warning never races ahead of the probe verdict.
	if !lazy.LastProbeWasFailure() {
		return
	}
	fmt.Fprintf(os.Stderr,
		"warning: AI worker not reachable at %s; agentic and embedding features "+
			"will activate on first request when the worker is up. "+
			"Start the worker with: make dev-worker\n",
		workerAddr)
	if err := lazy.LastProbeError(); err != nil {
		// Also log structured for ops; the stderr line is for the
		// dev who's tailing the API process.
		slog.Info("agent synth: boot probe failed; will lazy-retry on first request",
			"worker_addr", workerAddr, "err", err)
	}
}
