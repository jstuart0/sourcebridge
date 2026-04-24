// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexing

import (
	"testing"
)

func TestIsGitURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://github.com/foo/bar", true},
		{"http://gitlab.com/foo/bar", true},
		{"git@github.com:foo/bar.git", true},
		{"ssh://git@example.com/foo.git", true},
		{"git://example.com/foo.git", true},
		{"/abs/local/path", false},
		{"./relative/path", false},
		{"my-repo", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsGitURL(c.in); got != c.want {
			t.Errorf("IsGitURL(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeGitURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://github.com/foo/bar.git", "https://github.com/foo/bar"},
		{"https://user:pass@github.com/foo/bar.git", "https://github.com/foo/bar"},
		{"https://ghp_xxxx@github.com/foo/bar", "https://github.com/foo/bar"},
		{"git@github.com:foo/bar.git", "git@github.com:foo/bar"},
		{"https://gitlab.com/group/sub/repo", "https://gitlab.com/group/sub/repo"},
	}
	for _, c := range cases {
		if got := NormalizeGitURL(c.in); got != c.want {
			t.Errorf("NormalizeGitURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDeriveRepoName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://github.com/foo/bar.git", "bar"},
		{"/abs/path/my-repo", "my-repo"},
		{"./local/monorepo", "monorepo"},
		{"", "repo"},
	}
	for _, c := range cases {
		if got := deriveRepoName(c.in); got != c.want {
			t.Errorf("deriveRepoName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeRepoName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"my-repo", "my-repo"},
		{"foo/bar", "foo-bar"},
		{"with spaces", "with-spaces"},
		{"weird*chars!", "weirdchars"},
		{"", "repo"},
	}
	for _, c := range cases {
		if got := sanitizeRepoName(c.in); got != c.want {
			t.Errorf("sanitizeRepoName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
