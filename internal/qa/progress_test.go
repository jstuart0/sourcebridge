// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureEmitter is a tiny thread-safe ProgressEmitter that records
// every event for assertion in tests.
type captureEmitter struct {
	mu     sync.Mutex
	events []ProgressEvent
}

func (c *captureEmitter) Emit(e ProgressEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *captureEmitter) All() []ProgressEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ProgressEvent, len(c.events))
	copy(out, c.events)
	return out
}

func TestProgressEmitter_NoEmitterIsNoop(t *testing.T) {
	// No emitter attached — emitProgress should not panic or
	// allocate anything observable.
	ctx := context.Background()
	start := time.Now()
	emitProgress(ctx, start, "planning")
	emitProgress(ctx, start, "tool_call", withTool("search_evidence"))
	// If we got here without panicking, test passes.
}

func TestProgressEmitter_RoundTrip(t *testing.T) {
	cap := &captureEmitter{}
	ctx := WithProgressEmitter(context.Background(), cap)

	start := time.Now()
	emitProgress(ctx, start, "planning")
	emitProgress(ctx, start, "tool_call", withTool("search_evidence"))
	emitProgress(ctx, start, "tool_result", withTool("search_evidence"))
	emitProgress(ctx, start, "synthesizing")
	emitProgress(ctx, start, "done", withTermination("answer"))

	events := cap.All()
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d: %+v", len(events), events)
	}

	wantPhases := []string{"planning", "tool_call", "tool_result", "synthesizing", "done"}
	for i, want := range wantPhases {
		if events[i].Phase != want {
			t.Errorf("event[%d] phase = %q, want %q", i, events[i].Phase, want)
		}
	}
	if events[1].ToolName != "search_evidence" {
		t.Errorf("tool_call event should carry ToolName, got %q", events[1].ToolName)
	}
	if events[4].TerminationReason != "answer" {
		t.Errorf("done event should carry TerminationReason, got %q", events[4].TerminationReason)
	}
}

func TestProgressEventString_FormatsConsistently(t *testing.T) {
	cases := []struct {
		event ProgressEvent
		want  string
	}{
		{ProgressEvent{Phase: "planning"}, "[agent] planning…\n"},
		{ProgressEvent{Phase: "tool_call", ToolName: "search_evidence"}, "[agent] → search_evidence\n"},
		{ProgressEvent{Phase: "tool_result", ToolName: "search_evidence", DurationMs: 234}, "[agent] ← search_evidence (234ms)\n"},
		{ProgressEvent{Phase: "tool_result", ToolName: "search_evidence"}, "[agent] ← search_evidence\n"},
		{ProgressEvent{Phase: "synthesizing"}, "[agent] synthesizing answer…\n"},
		{ProgressEvent{Phase: "done", TerminationReason: "answer"}, "[agent] done (answer)\n"},
		{ProgressEvent{Phase: "done"}, "[agent] done\n"},
		{ProgressEvent{Phase: "unknown"}, "[agent] unknown\n"},
	}
	for _, c := range cases {
		got := ProgressEventString(c.event)
		if got != c.want {
			t.Errorf("ProgressEventString(%+v) = %q, want %q", c.event, got, c.want)
		}
	}
}

func TestProgressEmitter_ContextPropagation(t *testing.T) {
	cap := &captureEmitter{}
	ctx := WithProgressEmitter(context.Background(), cap)

	// Ensure the attached emitter comes back out.
	got := ProgressEmitterFromContext(ctx)
	if got == nil {
		t.Fatal("ProgressEmitterFromContext returned nil after WithProgressEmitter")
	}
	if got != cap {
		t.Error("returned emitter is not the one we attached")
	}

	// WithProgressEmitter(nil) is a no-op (doesn't panic, doesn't
	// overwrite an existing emitter with nil).
	if ctx2 := WithProgressEmitter(ctx, nil); ctx2 != ctx {
		t.Error("WithProgressEmitter(nil) should return the original context unchanged")
	}
}

// TestProgressEventString_RendersAllKnownPhases sanity-checks that
// every canonical phase has a non-empty rendering.
func TestProgressEventString_RendersAllKnownPhases(t *testing.T) {
	phases := []string{"planning", "tool_call", "tool_result", "synthesizing", "done"}
	for _, p := range phases {
		got := ProgressEventString(ProgressEvent{Phase: p})
		if got == "" || !strings.HasPrefix(got, "[agent]") {
			t.Errorf("phase %q renders to %q — expected a non-empty [agent] line", p, got)
		}
	}
}

