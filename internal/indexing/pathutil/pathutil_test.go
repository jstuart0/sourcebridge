// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package pathutil

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Frozen baselines — these are verbatim copies of the pre-existing
// implementations captured before the refactor so the parity tests remain
// stable even after the source files are rewritten to delegate.
// ---------------------------------------------------------------------------

// baseline_graphql_sanitizeRepoName is a verbatim copy of
// internal/api/graphql/helpers.go:sanitizeRepoName before the refactor.
func baseline_graphql_sanitizeRepoName(name string) string { //nolint:revive
	r := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	return r.Replace(name)
}

// baseline_indexing_sanitizeRepoName is a verbatim copy of
// internal/indexing/service.go:sanitizeRepoName before the refactor.
func baseline_indexing_sanitizeRepoName(name string) string { //nolint:revive
	if name == "" {
		return "repo"
	}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		case r == ' ', r == '/', r == '\\':
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "repo"
	}
	return string(out)
}

// baseline_qa_sanitizeRepoName is a verbatim copy of the pre-Slice-7
// sanitizeRepoNameForQA body from internal/api/rest/qa_deps.go.
// Slice 7 (b50c087) incorrectly delegated this to StrictPolicy, which is more
// aggressive (drops non-alphanumeric chars). The original body was:
//
//	strings.NewReplacer("/", "-", ":", "-").Replace(name)
//
// Only '/' and ':' are replaced; all other characters (spaces, backslashes,
// non-ASCII, punctuation) are preserved verbatim. This baseline is kept frozen
// so QALegacyPolicy can be verified against it.
func baseline_qa_sanitizeRepoName(name string) string { //nolint:revive
	r := strings.NewReplacer("/", "-", ":", "-")
	return r.Replace(name)
}

// ---------------------------------------------------------------------------
// Behavior-parity tests
// ---------------------------------------------------------------------------

// TestSanitizeRepoName_BehaviorParity verifies that the consolidated
// SanitizeRepoName function produces exactly the same output as each
// pre-existing implementation for every printable ASCII codepoint and a
// sampled set of non-ASCII inputs.
func TestSanitizeRepoName_BehaviorParity(t *testing.T) {
	// Printable ASCII: 0x20 (space) through 0x7E (~)
	var testInputs []string
	for cp := rune(0x20); cp <= 0x7E; cp++ {
		testInputs = append(testInputs, string(cp))
	}

	// Non-ASCII samples: emoji, non-Latin, NUL, control chars
	extra := []string{
		"",                      // empty
		"\x00",                  // NUL
		"\x01",                  // SOH control char
		"\x1f",                  // US control char
		"こんにちは",               // Hiragana/Katakana
		"привет",                // Cyrillic
		"🚀",                   // emoji (multi-byte)
		"user/repo",             // compound
		"my:project",            // colon
		"my repo name",          // space
		"back\\slash",           // backslash
		"a/b/c",                 // nested path
		"foo:bar/baz qux\\quux", // mixed separators
	}
	testInputs = append(testInputs, extra...)

	for _, input := range testInputs {
		// GraphQLLegacyPolicy must match the graphql baseline.
		got := SanitizeRepoName(input, GraphQLLegacyPolicy)
		want := baseline_graphql_sanitizeRepoName(input)
		if got != want {
			t.Errorf("GraphQLLegacyPolicy(%q): got %q, want %q", input, got, want)
		}

		// StrictPolicy must match the indexing baseline.
		got = SanitizeRepoName(input, StrictPolicy)
		want = baseline_indexing_sanitizeRepoName(input)
		if got != want {
			t.Errorf("StrictPolicy(%q): got %q, want %q", input, got, want)
		}

		// QALegacyPolicy must match the pre-Slice-7 QA baseline.
		// This guards against the regression introduced in b50c087 where
		// sanitizeRepoNameForQA was mapped to StrictPolicy — a stricter policy
		// that drops non-alphanumeric chars and rewrites spaces/backslashes,
		// breaking fallback cache-directory resolution for repo names with
		// colons, spaces, or other punctuation.
		got = SanitizeRepoName(input, QALegacyPolicy)
		want = baseline_qa_sanitizeRepoName(input)
		if got != want {
			t.Errorf("QALegacyPolicy(%q): got %q, want %q", input, got, want)
		}
	}

	// Spot-check the critical QA cases that motivated this fix.
	// These are the exact inputs where StrictPolicy and QALegacyPolicy diverge,
	// demonstrating why mapping the QA helper to StrictPolicy was incorrect.
	qaSpotChecks := []struct {
		input       string
		wantQA      string // QALegacyPolicy (correct)
		wantStrict  string // StrictPolicy (wrong for QA)
		shouldDiffer bool
	}{
		{"my:project", "my-project", "myproject", true},           // colon dropped by strict
		{"user/repo", "user-repo", "user-repo", false},             // slash: both policies agree
		{"my project", "my project", "my-project", true},          // space: preserved by QA, replaced by strict
		{"back\\slash", "back\\slash", "back-slash", true},         // backslash: preserved by QA, replaced by strict
		{"résumé", "résumé", "rsum", true},                        // non-ASCII: preserved by QA, dropped by strict
		{"foo:bar/baz qux\\quux", "foo-bar-baz qux\\quux", "foo-bar-baz-qux-quux", true},
	}
	for _, tc := range qaSpotChecks {
		gotQA := SanitizeRepoName(tc.input, QALegacyPolicy)
		gotStrict := SanitizeRepoName(tc.input, StrictPolicy)

		if gotQA != tc.wantQA {
			t.Errorf("QALegacyPolicy spot-check(%q): got %q, want %q", tc.input, gotQA, tc.wantQA)
		}
		if tc.shouldDiffer && gotQA == gotStrict {
			t.Errorf("QALegacyPolicy(%q)==StrictPolicy(%q)==%q — expected them to differ; QA policy is not strict enough or strict policy changed", tc.input, tc.input, gotQA)
		}
	}
}

// TestSafeJoinRepoPath_Basic covers happy-path joins.
func TestSafeJoinRepoPath_Basic(t *testing.T) {
	dir := t.TempDir()

	got, err := SafeJoinRepoPath(dir, "foo/bar.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "foo", "bar.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSafeJoinRepoPath_StripsDotSlash verifies that a leading "./" is stripped.
func TestSafeJoinRepoPath_StripsDotSlash(t *testing.T) {
	dir := t.TempDir()

	got, err := SafeJoinRepoPath(dir, "./main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "main.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSafeJoinRepoPath_RejectsAbsolute verifies that absolute relPaths are rejected.
func TestSafeJoinRepoPath_RejectsAbsolute(t *testing.T) {
	dir := t.TempDir()
	_, err := SafeJoinRepoPath(dir, "/etc/passwd")
	if err == nil {
		t.Error("expected error for absolute path, got nil")
	}
}

// TestSafeJoinRepoPath_RejectsTraversal verifies that path traversal is rejected.
func TestSafeJoinRepoPath_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	_, err := SafeJoinRepoPath(dir, "../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	}
}

// TestSafeJoinRepoPath_AllowsRootItself verifies that joining "" or "." returns the root.
func TestSafeJoinRepoPath_AllowsRootItself(t *testing.T) {
	dir := t.TempDir()
	got, err := SafeJoinRepoPath(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	abs, _ := filepath.Abs(dir)
	if got != abs {
		t.Errorf("got %q, want %q", got, abs)
	}
}

// TestIsGitURL covers all recognized URL patterns, including local .git
// directories that must NOT be classified as remote (codex M regression).
func TestIsGitURL(t *testing.T) {
	trueInputs := []string{
		// Scheme-prefixed — always remote.
		"http://github.com/user/repo",
		"https://github.com/user/repo",
		"git://github.com/user/repo",
		"git@github.com:user/repo",
		"ssh://git@github.com/user/repo",
		// Host-shaped .git suffix — remote shorthand.
		"github.com/user/repo.git",
		"gitlab.example.com/org/project.git",
	}
	falseInputs := []string{
		// Plain local paths.
		"/home/user/repo",
		"./local/repo",
		"C:\\repos\\myrepo",
		"not-a-url",
		"",
		// Local bare-repo paths with .git suffix — must remain local (codex M fix).
		"/Users/x/repos/repo.git",         // absolute local bare repo
		"/home/user/projects/mylib.git",    // absolute local bare repo
		"./repo.git",                        // relative local bare repo
		"../parent/repo.git",               // relative local bare repo (parent dir)
		"repo.git",                          // bare local name — no hostname, ambiguous → local
	}

	for _, s := range trueInputs {
		if !IsGitURL(s) {
			t.Errorf("IsGitURL(%q): want true, got false", s)
		}
	}
	for _, s := range falseInputs {
		if IsGitURL(s) {
			t.Errorf("IsGitURL(%q): want false, got true", s)
		}
	}
}

// TestSafeJoinRepoPath_WrittenFile verifies that the returned path can actually
// be used to create a file within the repo root.
func TestSafeJoinRepoPath_WrittenFile(t *testing.T) {
	dir := t.TempDir()
	joined, err := SafeJoinRepoPath(dir, "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(joined), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(joined, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CA-312: ValidateGitURLForClone — T5-T14
// ---------------------------------------------------------------------------

// stubLookupPublic returns a single public IP for any host.
func stubLookupPublic(_ string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("1.2.3.4")}, nil
}

// stubLookupFail simulates a DNS lookup failure.
func stubLookupFail(_ string) ([]net.IP, error) {
	return nil, errors.New("dns: no such host")
}

// T5 — blocked schemes (http://, file://, git://, ftp://)
func TestValidateGitURL_BlockedSchemes(t *testing.T) {
	blocked := []string{
		"http://github.com/org/repo.git",
		"file:///etc/passwd",
		"git://github.com/org/repo.git",
		"ftp://github.com/repo",
		"javascript:alert(1)",
		"data:text/plain,hello",
	}
	for _, u := range blocked {
		err := ValidateGitURLForClone(u, false, stubLookupPublic)
		if !errors.Is(err, ErrSchemeNotAllowed) {
			t.Errorf("URL %q: want ErrSchemeNotAllowed, got %v", u, err)
		}
	}
}

// T6 — public HTTPS allowed (stubbed lookup returns public IP)
func TestValidateGitURL_PublicHTTPS_Allowed(t *testing.T) {
	err := ValidateGitURLForClone("https://github.com/org/repo.git", false, stubLookupPublic)
	if err != nil {
		t.Errorf("public HTTPS should be allowed, got: %v", err)
	}
}

// T7 — private HTTPS denied (table-driven over all denylist ranges)
func TestValidateGitURL_PrivateIPs_Denied(t *testing.T) {
	privateIPs := []string{
		"10.0.0.1",
		"192.168.1.1",
		"172.16.0.1",
		"127.0.0.1",
		"::1",
		"169.254.169.254",
		"100.64.0.1",
		"fc00::1",
		"fe80::1",
	}
	for _, ip := range privateIPs {
		ipCopy := ip
		stubPrivate := func(_ string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(ipCopy)}, nil
		}
		err := ValidateGitURLForClone("https://internal.example.com/repo.git", false, stubPrivate)
		if !errors.Is(err, ErrPrivateIPNotAllowed) {
			t.Errorf("IP %s: want ErrPrivateIPNotAllowed, got %v", ip, err)
		}
	}
}

// T8 — private IPs allowed with opt-in flag
func TestValidateGitURL_PrivateIPs_AllowedWithOptIn(t *testing.T) {
	privateIPs := []string{
		"10.0.0.1",
		"192.168.1.1",
		"172.16.0.1",
		"127.0.0.1",
	}
	for _, ip := range privateIPs {
		ipCopy := ip
		stubPrivate := func(_ string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(ipCopy)}, nil
		}
		// allowPrivate=true should skip IP validation
		err := ValidateGitURLForClone("https://gitea.internal/org/repo.git", true, stubPrivate)
		if err != nil {
			t.Errorf("IP %s with allowPrivate=true should be allowed, got: %v", ip, err)
		}
	}
}

// T9 — SCP-form git@ with private IP denied
func TestValidateGitURL_SCPForm_PrivateIP_Denied(t *testing.T) {
	cases := []struct {
		url string
		ip  string
	}{
		{"git@10.0.0.1:org/repo.git", "10.0.0.1"},
		{"git@169.254.169.254:admin/secrets.git", "169.254.169.254"},
	}
	for _, tc := range cases {
		ipCopy := tc.ip
		stubPrivate := func(_ string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(ipCopy)}, nil
		}
		err := ValidateGitURLForClone(tc.url, false, stubPrivate)
		if !errors.Is(err, ErrPrivateIPNotAllowed) {
			t.Errorf("SCP form %q: want ErrPrivateIPNotAllowed, got %v", tc.url, err)
		}
	}
}

// T10 — SCP-form git@ with public IP allowed
func TestValidateGitURL_SCPForm_Public_Allowed(t *testing.T) {
	err := ValidateGitURLForClone("git@github.com:org/repo.git", false, stubLookupPublic)
	if err != nil {
		t.Errorf("SCP form with public IP should be allowed, got: %v", err)
	}
}

// T11 — ssh:// form with private and public IPs
func TestValidateGitURL_SSHForm(t *testing.T) {
	stubPrivate := func(_ string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.1")}, nil
	}
	if err := ValidateGitURLForClone("ssh://git@10.0.0.1/repo.git", false, stubPrivate); !errors.Is(err, ErrPrivateIPNotAllowed) {
		t.Errorf("ssh:// to private IP: want ErrPrivateIPNotAllowed, got %v", err)
	}
	if err := ValidateGitURLForClone("ssh://git@github.com/repo.git", false, stubLookupPublic); err != nil {
		t.Errorf("ssh:// to public host should be allowed, got: %v", err)
	}
}

// T12 — DNS lookup failure → ErrHostnameUnresolvable (fail-closed)
func TestValidateGitURL_DNSFailure_FailClosed(t *testing.T) {
	err := ValidateGitURLForClone("https://no-such-host.example.invalid/repo.git", false, stubLookupFail)
	if !errors.Is(err, ErrHostnameUnresolvable) {
		t.Errorf("DNS failure: want ErrHostnameUnresolvable, got %v", err)
	}
}

// T13 — multi-IP rebind defense: one public + one private → rejected
func TestValidateGitURL_MultiIP_AnyPrivateFails(t *testing.T) {
	stubMixed := func(_ string) ([]net.IP, error) {
		return []net.IP{
			net.ParseIP("1.2.3.4"),   // public
			net.ParseIP("10.0.0.1"), // RFC1918 — should trigger rejection
		}, nil
	}
	err := ValidateGitURLForClone("https://dns-rebind.example.com/repo.git", false, stubMixed)
	if !errors.Is(err, ErrPrivateIPNotAllowed) {
		t.Errorf("mixed IPs (one private): want ErrPrivateIPNotAllowed, got %v", err)
	}
}

// T14 — allowPrivate=true + http:// scheme → still rejected (gates independent)
func TestValidateGitURL_AllowPrivateDoesNotUnlockHTTP(t *testing.T) {
	err := ValidateGitURLForClone("http://gitea.internal/org/repo.git", true, stubLookupPublic)
	if !errors.Is(err, ErrSchemeNotAllowed) {
		t.Errorf("http:// with allowPrivate=true: want ErrSchemeNotAllowed, got %v", err)
	}
}

// T15 — codex r2 H1: 0.0.0.0 and :: (unspecified addresses) must be denied.
// On common OS stacks these map to the local host and bypass loopback detection.
func TestValidateGitURL_UnspecifiedAddresses_Denied(t *testing.T) {
	cases := []struct {
		name string
		ip   string
	}{
		{"IPv4 unspecified (0.0.0.0)", "0.0.0.0"},
		{"IPv6 unspecified (::)", "::"},
	}
	for _, tc := range cases {
		ipCopy := tc.ip
		stubUnspecified := func(_ string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(ipCopy)}, nil
		}
		err := ValidateGitURLForClone("https://evil.example.com/repo.git", false, stubUnspecified)
		if !errors.Is(err, ErrPrivateIPNotAllowed) {
			t.Errorf("%s: want ErrPrivateIPNotAllowed, got %v", tc.name, err)
		}
	}
}

// T16 — multicast addresses must be denied (git over multicast is not legitimate).
func TestValidateGitURL_MulticastAddresses_Denied(t *testing.T) {
	cases := []struct {
		name string
		ip   string
	}{
		{"IPv4 multicast (224.0.0.1)", "224.0.0.1"},
		{"IPv6 multicast (ff02::1)", "ff02::1"},
		{"IPv6 interface-local multicast (ff01::1)", "ff01::1"},
	}
	for _, tc := range cases {
		ipCopy := tc.ip
		stubMulticast := func(_ string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(ipCopy)}, nil
		}
		err := ValidateGitURLForClone("https://evil.example.com/repo.git", false, stubMulticast)
		if !errors.Is(err, ErrPrivateIPNotAllowed) {
			t.Errorf("%s: want ErrPrivateIPNotAllowed, got %v", tc.name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// ValidateLLMBaseURL tests (CA-214)
// ---------------------------------------------------------------------------

// TestValidateLLMBaseURL_Matrix covers the 12-case accept/reject matrix
// for both allowPrivate=true and allowPrivate=false modes.
func TestValidateLLMBaseURL_Matrix(t *testing.T) {
	// stubLookup builds a LookupIPFunc that returns the given IP string.
	stubLookup := func(ipStr string) LookupIPFunc {
		return func(_ string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(ipStr)}, nil
		}
	}

	type tc struct {
		name          string
		url           string
		allowPrivate  bool
		lookupResult  string // empty = use real DNS; otherwise stub this IP
		wantErr       error  // nil = no error expected
	}

	cases := []tc{
		// Localhost — accepted when allowPrivate=true, rejected when false.
		{"localhost allowPrivate=true", "http://localhost:11434/v1", true, "127.0.0.1", nil},
		{"localhost allowPrivate=false", "http://localhost:11434/v1", false, "127.0.0.1", ErrLLMPrivateIPNotAllowed},
		// 127.0.0.1 loopback
		{"127.0.0.1 allowPrivate=true", "http://127.0.0.1:8080", true, "127.0.0.1", nil},
		{"127.0.0.1 allowPrivate=false", "http://127.0.0.1:8080", false, "", ErrLLMPrivateIPNotAllowed},
		// RFC1918 — 10.x
		{"10.x allowPrivate=true", "http://10.0.0.5:8080", true, "10.0.0.5", nil},
		{"10.x allowPrivate=false", "http://10.0.0.5:8080", false, "", ErrLLMPrivateIPNotAllowed},
		// RFC1918 — 172.16.x
		{"172.16.x allowPrivate=true", "http://172.16.0.5:8080", true, "172.16.0.5", nil},
		{"172.16.x allowPrivate=false", "http://172.16.0.5:8080", false, "", ErrLLMPrivateIPNotAllowed},
		// RFC1918 — 192.168.x
		{"192.168.x allowPrivate=true", "http://192.168.0.5:8080", true, "192.168.0.5", nil},
		{"192.168.x allowPrivate=false", "http://192.168.0.5:8080", false, "", ErrLLMPrivateIPNotAllowed},
		// Link-local / IMDS — 169.254.x
		{"169.254.x (IMDS) allowPrivate=true", "http://169.254.169.254/latest/meta-data/", true, "169.254.169.254", nil},
		{"169.254.x (IMDS) allowPrivate=false", "http://169.254.169.254/latest/meta-data/", false, "", ErrLLMPrivateIPNotAllowed},
		// CGNAT — 100.64.x
		{"100.64.x (CGNAT) allowPrivate=true", "http://100.64.0.5:8080", true, "100.64.0.5", nil},
		{"100.64.x (CGNAT) allowPrivate=false", "http://100.64.0.5:8080", false, "", ErrLLMPrivateIPNotAllowed},
		// IPv6 loopback — ::1
		{"[::1] allowPrivate=true", "http://[::1]:8080", true, "", nil},
		{"[::1] allowPrivate=false", "http://[::1]:8080", false, "", ErrLLMPrivateIPNotAllowed},
		// IPv6 multicast
		{"[ff00::1] allowPrivate=true", "http://[ff00::1]:8080", true, "", nil},
		{"[ff00::1] allowPrivate=false", "http://[ff00::1]:8080", false, "", ErrLLMPrivateIPNotAllowed},
		// Unspecified — 0.0.0.0
		{"0.0.0.0 allowPrivate=true", "http://0.0.0.0:8080", true, "", nil},
		{"0.0.0.0 allowPrivate=false", "http://0.0.0.0:8080", false, "", ErrLLMPrivateIPNotAllowed},
		// Public URL — always accepted
		{"openai always accepted", "https://api.openai.com/v1", true, "104.18.0.1", nil},
		{"openai always accepted strict", "https://api.openai.com/v1", false, "104.18.0.1", nil},
		// Bad scheme — always rejected
		{"ftp scheme always rejected", "ftp://example.com", true, "", ErrLLMSchemeNotAllowed},
		{"ftp scheme strict rejected", "ftp://example.com", false, "", ErrLLMSchemeNotAllowed},
		// Empty URL — always accepted (means "use provider default")
		{"empty url allowPrivate=true", "", true, "", nil},
		{"empty url allowPrivate=false", "", false, "", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var lookup LookupIPFunc
			if c.lookupResult != "" {
				ip := c.lookupResult
				lookup = stubLookup(ip)
			} else if !c.allowPrivate {
				// For tests where allowPrivate=false and the URL has a bare IP,
				// no DNS lookup happens — ValidateLLMBaseURL does ip.IsPrivate
				// directly. Pass nil to use real DNS only for hostname cases.
				lookup = nil
			}
			// For bare IPs in the URL, ValidateLLMBaseURL parses them directly
			// without a DNS lookup — the stub is only needed for hostname tests.
			err := ValidateLLMBaseURL(c.url, c.allowPrivate, lookup)
			if c.wantErr == nil {
				if err != nil {
					t.Errorf("want no error, got: %v", err)
				}
			} else {
				if !errors.Is(err, c.wantErr) {
					t.Errorf("want %v, got: %v", c.wantErr, err)
				}
			}
		})
	}
}

// TestValidateLLMBaseURL_EmptyAccepted verifies empty URL is a no-op.
func TestValidateLLMBaseURL_EmptyAccepted(t *testing.T) {
	if err := ValidateLLMBaseURL("", false, nil); err != nil {
		t.Fatalf("empty URL should be accepted, got: %v", err)
	}
}
