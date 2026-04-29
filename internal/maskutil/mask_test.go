// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package maskutil

import "testing"

func TestToken(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "****"},
		{"shortish", "12345678", "****"},
		{"just-over", "123456789", "1234...6789"},
		{"anthropic-style", "sk-ant-api03-AAAA-BBBB-CCCC-DDDD", "sk-a...DDDD"},
		{"openai-style", "sk-AAAAAAAAAAAA", "sk-A...AAAA"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Token(c.input); got != c.want {
				t.Errorf("Token(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}
