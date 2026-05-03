// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package source

import "testing"

func TestSliceLines(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5"

	cases := []struct {
		name    string
		content string
		start   int
		end     int
		want    string
	}{
		{
			name:    "empty content",
			content: "",
			start:   1,
			end:     3,
			want:    "",
		},
		{
			name:    "start zero",
			content: content,
			start:   0,
			end:     2,
			want:    "",
		},
		{
			name:    "start negative",
			content: content,
			start:   -3,
			end:     2,
			want:    "",
		},
		{
			name:    "end less than start",
			content: content,
			start:   3,
			end:     2,
			want:    "",
		},
		{
			name:    "end equal to zero",
			content: content,
			start:   1,
			end:     0,
			want:    "",
		},
		{
			name:    "start beyond file length",
			content: content,
			start:   100,
			end:     200,
			want:    "",
		},
		{
			name:    "end beyond file clamps to EOF",
			content: content,
			start:   4,
			end:     100,
			want:    "line4\nline5",
		},
		{
			name:    "normal slice middle",
			content: content,
			start:   2,
			end:     4,
			want:    "line2\nline3\nline4",
		},
		{
			name:    "single line",
			content: content,
			start:   3,
			end:     3,
			want:    "line3",
		},
		{
			name:    "exact EOF",
			content: content,
			start:   5,
			end:     5,
			want:    "line5",
		},
		{
			name:    "single line file",
			content: "only",
			start:   1,
			end:     1,
			want:    "only",
		},
		{
			name:    "start equals end beyond file",
			content: "a\nb",
			start:   3,
			end:     3,
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SliceLines(tc.content, tc.start, tc.end)
			if got != tc.want {
				t.Errorf("SliceLines(content, %d, %d) = %q, want %q", tc.start, tc.end, got, tc.want)
			}
		})
	}
}
