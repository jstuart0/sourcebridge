// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"bufio"
	"io"
	"strings"
)

// sseEvent is a parsed Server-Sent Event with the standard fields.
type sseEvent struct {
	Event string // event: <name>; defaults to "message" per spec
	ID    string // id: <id>; informational here, we don't drive retry
	Data  string // multiple data: lines concatenated with "\n" per spec
}

// parseSSE returns a channel that emits one sseEvent per logical event in r.
// The channel is closed when r returns EOF or any non-EOF error.
//
// Spec rules implemented:
//   - Lines starting with ":" are comments (typically keep-alives) — ignore.
//   - "data: <value>" appends to the current event's Data; multiple data
//     lines on one event are joined with "\n".
//   - "event: <name>" sets the event name.
//   - "id: <id>" sets the event id.
//   - "retry: <ms>" is parseable per spec but we don't reconnect from the
//     proxy, so it is recorded only as a pass-through (not exposed).
//   - A blank line emits the current event and resets accumulators.
//   - EOF flushes any in-flight accumulators iff Data is non-empty.
//
// We do NOT use a regex or third-party SSE library — net/http's response
// body is bufio-friendly and the format is small enough that hand-rolled
// parsing keeps dependencies and surface area minimal.
func parseSSE(r io.Reader) <-chan sseEvent {
	out := make(chan sseEvent)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(r)
		// Generous max line size: progress messages can carry partial
		// tool-call results in their JSON.
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024) // 1 MiB

		var current sseEvent
		dataLines := []string{}

		flush := func() {
			if len(dataLines) == 0 && current.Event == "" && current.ID == "" {
				return // nothing to emit
			}
			current.Data = strings.Join(dataLines, "\n")
			out <- current
			current = sseEvent{}
			dataLines = dataLines[:0]
		}

		for scanner.Scan() {
			line := scanner.Text()
			// Strip a single trailing CR if present (CRLF tolerance).
			line = strings.TrimRight(line, "\r")

			// Blank line — emit and reset.
			if line == "" {
				flush()
				continue
			}

			// Comment.
			if strings.HasPrefix(line, ":") {
				continue
			}

			// Field: value, where the colon may or may not be followed by a space.
			// Spec: if a line has no colon, the whole line is the field name with
			// empty value.
			var field, value string
			if i := strings.IndexByte(line, ':'); i >= 0 {
				field = line[:i]
				value = line[i+1:]
				// Per spec: if value starts with a single space, remove it.
				if strings.HasPrefix(value, " ") {
					value = value[1:]
				}
			} else {
				field = line
			}

			switch field {
			case "event":
				current.Event = value
			case "id":
				current.ID = value
			case "data":
				dataLines = append(dataLines, value)
			case "retry":
				// Recorded but ignored (proxy doesn't reconnect).
			default:
				// Unknown field — spec says ignore.
			}
		}
		// EOF / error — flush a final pending event (some servers omit the
		// trailing blank line before closing).
		flush()
	}()
	return out
}
