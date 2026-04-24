// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"fmt"
	"time"
)

// Phase 2.5 follow-on — structured progress events from the agentic
// retrieval loop.
//
// Callers that want to observe loop progress attach a ProgressEmitter
// to the context via WithProgressEmitter. The agent loop pulls it out
// and emits structured phase events at key boundaries (planning,
// tool_call, tool_result, synthesizing, done).
//
// Emit is best-effort and non-blocking — the loop never stalls on a
// slow consumer. When no emitter is attached, calls to Emit become
// no-ops, so the loop doesn't need to branch.

// ProgressEmitter receives structured progress events during the
// agentic retrieval loop. Implementations are responsible for any
// forwarding (to MCP ContentEmitter, to logs, to a UI, etc.).
type ProgressEmitter interface {
	// Emit records one phase event. Must not block.
	Emit(event ProgressEvent)
}

// ProgressEvent is the public shape clients see. Kept narrow so
// adding new fields is safe — consumers match on `Phase` and read
// optional fields only when present.
type ProgressEvent struct {
	// Phase is a stable identifier. Clients compare literal values
	// rather than enum constants so new phases don't require a
	// coordinated release.
	//
	// Canonical values (2026-04-24):
	//   "planning"     — about to call the synthesizer
	//   "tool_call"    — agent invoked a tool; ToolName set
	//   "tool_result"  — tool returned; ToolName + DurationMs set
	//   "synthesizing" — final answer generation has started
	//   "done"         — loop terminated; TerminationReason set
	Phase              string
	ToolName           string
	Detail             string
	ElapsedMs          int64
	DurationMs         int64
	TerminationReason  string
}

// progressEmitterKey namespaces the emitter in context.Context.
type progressEmitterKey struct{}

// WithProgressEmitter attaches a ProgressEmitter so the agent loop
// can find it. Use on the context you pass to Orchestrator.Ask.
func WithProgressEmitter(ctx context.Context, e ProgressEmitter) context.Context {
	if e == nil {
		return ctx
	}
	return context.WithValue(ctx, progressEmitterKey{}, e)
}

// ProgressEmitterFromContext returns the attached emitter or nil.
func ProgressEmitterFromContext(ctx context.Context) ProgressEmitter {
	v, _ := ctx.Value(progressEmitterKey{}).(ProgressEmitter)
	return v
}

// emitProgress is a tiny helper used inside the loop. Handles the
// "no emitter attached" case inline so call sites stay readable.
func emitProgress(ctx context.Context, start time.Time, phase string, opts ...progressOption) {
	e := ProgressEmitterFromContext(ctx)
	if e == nil {
		return
	}
	event := ProgressEvent{
		Phase:     phase,
		ElapsedMs: time.Since(start).Milliseconds(),
	}
	for _, opt := range opts {
		opt(&event)
	}
	e.Emit(event)
}

type progressOption func(*ProgressEvent)

func withTool(name string) progressOption {
	return func(e *ProgressEvent) { e.ToolName = name }
}

func withDetail(s string) progressOption {
	return func(e *ProgressEvent) { e.Detail = s }
}

func withDuration(d time.Duration) progressOption {
	return func(e *ProgressEvent) { e.DurationMs = d.Milliseconds() }
}

func withTermination(reason string) progressOption {
	return func(e *ProgressEvent) { e.TerminationReason = reason }
}

// ProgressEventString is a convenience for adapters that want to
// render an event to a single line of text. Used by the MCP relay
// to push a human-readable delta to ContentEmitter.
func ProgressEventString(e ProgressEvent) string {
	switch e.Phase {
	case "tool_call":
		if e.ToolName != "" {
			return fmt.Sprintf("[agent] → %s\n", e.ToolName)
		}
		return "[agent] → tool\n"
	case "tool_result":
		if e.ToolName != "" && e.DurationMs > 0 {
			return fmt.Sprintf("[agent] ← %s (%dms)\n", e.ToolName, e.DurationMs)
		}
		if e.ToolName != "" {
			return fmt.Sprintf("[agent] ← %s\n", e.ToolName)
		}
		return "[agent] ← tool result\n"
	case "synthesizing":
		return "[agent] synthesizing answer…\n"
	case "done":
		if e.TerminationReason != "" {
			return fmt.Sprintf("[agent] done (%s)\n", e.TerminationReason)
		}
		return "[agent] done\n"
	case "planning":
		return "[agent] planning…\n"
	default:
		return fmt.Sprintf("[agent] %s\n", e.Phase)
	}
}
