// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"strings"
	"testing"
)

// TestParseSSE_Basic asserts a single data event flushes on blank line.
func TestParseSSE_Basic(t *testing.T) {
	input := "event: message\ndata: hello\n\n"
	events := drainSSE(input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Event != "message" || events[0].Data != "hello" {
		t.Errorf("unexpected event: %+v", events[0])
	}
}

// TestParseSSE_MultiLineDataConcat asserts multi-line data is joined with
// "\n" per spec (codex r1 M1).
func TestParseSSE_MultiLineDataConcat(t *testing.T) {
	input := "data: line1\ndata: line2\ndata: line3\n\n"
	events := drainSSE(input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	want := "line1\nline2\nline3"
	if events[0].Data != want {
		t.Errorf("data concat:\n got: %q\nwant: %q", events[0].Data, want)
	}
}

// TestParseSSE_CommentsIgnored asserts ":" comment lines (keep-alives) are
// ignored.
func TestParseSSE_CommentsIgnored(t *testing.T) {
	input := ": keep-alive\ndata: payload\n: another comment\n\n"
	events := drainSSE(input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "payload" {
		t.Errorf("got Data=%q; want 'payload'", events[0].Data)
	}
}

// TestParseSSE_EOFFlushesPendingEvent asserts that an event without a
// trailing blank line still emits on EOF.
func TestParseSSE_EOFFlushesPendingEvent(t *testing.T) {
	input := "data: trailing\n"
	events := drainSSE(input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event on EOF flush, got %d", len(events))
	}
	if events[0].Data != "trailing" {
		t.Errorf("got Data=%q", events[0].Data)
	}
}

// TestParseSSE_MultipleEvents asserts multiple events emit in order.
func TestParseSSE_MultipleEvents(t *testing.T) {
	input := "data: first\n\ndata: second\n\ndata: third\n\n"
	events := drainSSE(input)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	for i, want := range []string{"first", "second", "third"} {
		if events[i].Data != want {
			t.Errorf("event[%d].Data = %q; want %q", i, events[i].Data, want)
		}
	}
}

// TestParseSSE_CRLF asserts CRLF line endings are tolerated.
func TestParseSSE_CRLF(t *testing.T) {
	input := "data: hello\r\n\r\n"
	events := drainSSE(input)
	if len(events) != 1 || events[0].Data != "hello" {
		t.Fatalf("CRLF not handled: %+v", events)
	}
}

// TestParseSSE_FieldWithNoSpaceAfterColon asserts "data:value" (no space)
// still parses; spec says strip a single leading space if present.
func TestParseSSE_FieldWithNoSpaceAfterColon(t *testing.T) {
	input := "data:nosp\n\n"
	events := drainSSE(input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "nosp" {
		t.Errorf("got Data=%q; want 'nosp'", events[0].Data)
	}
}

// TestParseSSE_UnknownFieldsIgnored asserts retry/event/id/unknown fields
// don't break the parse.
func TestParseSSE_UnknownFieldsIgnored(t *testing.T) {
	input := "id: 99\nretry: 5000\ncustom: ignored\ndata: payload\n\n"
	events := drainSSE(input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event")
	}
	if events[0].Data != "payload" || events[0].ID != "99" {
		t.Errorf("got %+v", events[0])
	}
}

// drainSSE is a tiny helper that consumes the channel.
func drainSSE(input string) []sseEvent {
	out := []sseEvent{}
	for ev := range parseSSE(strings.NewReader(input)) {
		out = append(out, ev)
	}
	return out
}
