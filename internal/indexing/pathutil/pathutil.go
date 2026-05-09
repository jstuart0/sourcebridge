// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package pathutil provides shared path and repository-name helpers used by
// the indexing, GraphQL, and REST API layers.
package pathutil

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// SafeJoinRepoPath joins a repo root and a relative path, ensuring the result
// stays inside the repo root. Returns an error for absolute paths or path
// traversal attempts.
//
// This consolidates safeJoinPath (internal/api/graphql/helpers.go) and
// safeJoinRepoPath (internal/api/rest/qa_deps.go), which were structurally
// identical.
func SafeJoinRepoPath(repoRoot, relPath string) (string, error) {
	relPath = strings.TrimPrefix(relPath, "./")
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute path not allowed: %s", relPath)
	}
	joined := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolving repo root: %w", err)
	}
	if absJoined != absRoot && !strings.HasPrefix(absJoined, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal rejected: %s", relPath)
	}
	return absJoined, nil
}

// SanitizePolicy controls how SanitizeRepoName treats characters.
type SanitizePolicy int

const (
	// StrictPolicy keeps alphanumeric characters plus '-', '_', and '.'.
	// Spaces, forward-slashes, and backslashes become '-'; everything else
	// is dropped. An empty result falls back to "repo".
	// Matches the pre-existing behavior in internal/indexing/service.go.
	StrictPolicy SanitizePolicy = iota

	// GraphQLLegacyPolicy replaces '/', '\', ' ', and ':' with '-' and
	// preserves all other characters including non-ASCII.
	// Matches the pre-existing behavior in internal/api/graphql/helpers.go.
	GraphQLLegacyPolicy

	// QALegacyPolicy replaces only '/' and ':' with '-' and preserves
	// everything else including spaces, backslashes, and non-ASCII.
	// Matches the pre-existing behavior of sanitizeRepoNameForQA in
	// internal/api/rest/qa_deps.go before Slice 7 (b50c087), which used:
	//   strings.NewReplacer("/", "-", ":", "-").Replace(name)
	// Used to compute fallback QA cache-directory paths so existing on-disk
	// directories remain resolvable after the refactor.
	QALegacyPolicy
)

// SanitizeRepoName returns a filesystem-safe form of a repo name according to
// the given policy.
func SanitizeRepoName(name string, policy SanitizePolicy) string {
	switch policy {
	case GraphQLLegacyPolicy:
		r := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
		return r.Replace(name)
	case QALegacyPolicy:
		r := strings.NewReplacer("/", "-", ":", "-")
		return r.Replace(name)
	default: // StrictPolicy
		if name == "" {
			return "repo"
		}
		out := make([]rune, 0, len(name))
		for _, r := range name {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				r == '-', r == '_', r == '.':
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
}

// --- SSRF / git-clone URL validation (CA-312) ---

// LookupIPFunc is a function type matching net.LookupIP, used for testing.
type LookupIPFunc func(host string) ([]net.IP, error)

// Sentinel errors returned by ValidateGitURLForClone.
var (
	// ErrSchemeNotAllowed is returned when the URL uses a non-allowlisted scheme
	// (e.g. http://, file://, git://). Only https://, ssh://, and the SCP-form
	// git@host:path are permitted.
	ErrSchemeNotAllowed = errors.New("git URL scheme not allowed; use https:// or ssh://")

	// ErrPrivateIPNotAllowed is returned when the resolved hostname maps to a
	// private / loopback / link-local / CGNAT / ULA address. Use
	// SOURCEBRIDGE_INDEXING_ALLOW_PRIVATE_GIT_HOSTS=true to opt in for
	// self-hosted Forgejo / Gitea on internal networks (not safe for
	// multi-tenant public deploys).
	ErrPrivateIPNotAllowed = errors.New("git URL hostname resolves to a private IP; set SOURCEBRIDGE_INDEXING_ALLOW_PRIVATE_GIT_HOSTS=true for self-hosted internal repos (not safe for multi-tenant deploys)")

	// ErrHostnameUnresolvable is returned when the DNS lookup for the hostname
	// fails. Fail-closed: an unresolvable hostname may be a mis-typed private
	// host or a DNS rebind attack in progress.
	ErrHostnameUnresolvable = errors.New("git URL hostname is unresolvable; check DNS or use an IP address directly")
)

// cgnatBlock is 100.64.0.0/10 (RFC 6598 — Carrier-Grade NAT shared address
// space). stdlib net.IP.IsPrivate() does not classify this range, but an
// egress to a CGNAT block from a cloud API pod is still an SSRF risk.
var cgnatBlock = func() *net.IPNet {
	_, cidr, _ := net.ParseCIDR("100.64.0.0/10")
	return cidr
}()

// ulaBlock is fc00::/7 (RFC 4193 — IPv6 Unique Local Addresses).
// stdlib net.IP.IsPrivate() covers this, but we check explicitly for clarity.
var ulaBlock = func() *net.IPNet {
	_, cidr, _ := net.ParseCIDR("fc00::/7")
	return cidr
}()

// isPrivateOrInternalIP returns true if the given IP is in any denylist range:
// RFC1918, loopback, link-local unicast, CGNAT, ULA, unspecified (0.0.0.0/::),
// multicast, or interface-local multicast.
func isPrivateOrInternalIP(ip net.IP) bool {
	// stdlib covers: 10/8, 172.16/12, 192.168/16, 127/8, ::1, 169.254/16,
	// fe80::/10, fc00::/7.
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	// Unspecified: 0.0.0.0 or :: — on many stacks these resolve to the local
	// host and bypass loopback detection.
	if ip.IsUnspecified() {
		return true
	}
	// Multicast: git over multicast is never legitimate; block 224.0.0.0/4 and
	// ff00::/8 to prevent exotic routing tricks.
	if ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// CGNAT: 100.64.0.0/10 — not covered by stdlib IsPrivate.
	if cgnatBlock != nil && cgnatBlock.Contains(ip) {
		return true
	}
	return false
}

// extractHostname returns the hostname from a git URL string.
//
//   - https://host/... or ssh://host/...   → host (from url.Parse)
//   - git@host:path                        → host (SCP-form; left side of first colon after '@')
//   - anything else                        → "", ErrSchemeNotAllowed
func extractHostname(rawURL string) (string, error) {
	if strings.HasPrefix(rawURL, "https://") || strings.HasPrefix(rawURL, "ssh://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return "", fmt.Errorf("invalid URL: %w", err)
		}
		return u.Hostname(), nil
	}
	// SCP-form: git@host:path — must contain '@' and ':'
	if strings.HasPrefix(rawURL, "git@") {
		// Strip leading "git@"
		rest := strings.TrimPrefix(rawURL, "git@")
		// Find the colon that separates host from path.
		colonIdx := strings.Index(rest, ":")
		if colonIdx < 0 {
			return "", ErrSchemeNotAllowed
		}
		return rest[:colonIdx], nil
	}
	return "", ErrSchemeNotAllowed
}

// ValidateGitURLForClone validates a git URL before passing it to git clone.
//
// Two independent gates:
//  1. Scheme/form allowlist: https://, ssh://, and SCP-form (git@host:path)
//     are allowed; http://, file://, git://, ftp://, etc. are rejected with
//     ErrSchemeNotAllowed regardless of allowPrivate.
//  2. IP denylist: when allowPrivate is false, the hostname is resolved via
//     lookupIP (defaults to net.LookupIP in production callers). Any resolved
//     IP in a private/internal range causes ErrPrivateIPNotAllowed. Multi-IP
//     semantics: ANY private IP in the result rejects the request (defends
//     against split-horizon DNS rebinding where one A record is public and
//     another is RFC1918).
//
// The lookupIP parameter is injectable for testing. Pass nil to use the
// default net.LookupIP.
func ValidateGitURLForClone(rawURL string, allowPrivate bool, lookupIP LookupIPFunc) error {
	if lookupIP == nil {
		lookupIP = net.LookupIP
	}

	host, err := extractHostname(rawURL)
	if err != nil {
		// extractHostname returns ErrSchemeNotAllowed for disallowed schemes.
		return err
	}

	if allowPrivate {
		// Skip IP validation when the operator has explicitly opted in.
		// The scheme gate already fired above — http:// is still rejected
		// even with allowPrivate=true.
		return nil
	}

	ips, err := lookupIP(host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("%w: %s", ErrHostnameUnresolvable, host)
	}

	for _, ip := range ips {
		if isPrivateOrInternalIP(ip) {
			return fmt.Errorf("%w: %s resolves to %s", ErrPrivateIPNotAllowed, host, ip)
		}
	}

	return nil
}

// IsGitURL returns true if s looks like a remote git URL.
//
// Scheme-prefixed URLs (http://, https://, git://, ssh://, git@) are always
// classified as remote. For .git-suffix strings the function guards against
// local bare-repo paths: absolute paths and explicit relative paths
// (./repo.git, ../parent/repo.git) remain local. A bare name without a
// slash (repo.git) is also treated as local because it is ambiguous without
// network context. Only host-shaped strings with a dot before the first slash
// are classified as remote shorthand (e.g. github.com/user/repo.git).
//
// Pre-Slice-7 the indexing layer only checked URL schemes; the graphql layer
// also accepted the .git suffix but only on host-shaped strings. The consolidated
// function preserves both behaviors while fixing the regression introduced in
// b50c087 where /abs/path/repo.git was misclassified as remote.
func IsGitURL(s string) bool {
	// Scheme-prefixed URLs are unambiguously remote.
	if strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "ssh://") {
		return true
	}

	// .git suffix: only treat as remote when the string is host-shaped.
	// Absolute paths, explicit relative paths, and bare names are local.
	if strings.HasSuffix(s, ".git") {
		if filepath.IsAbs(s) {
			return false // /abs/path/repo.git — local bare repo
		}
		if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
			return false // ./repo.git or ../parent/repo.git — local bare repo
		}
		// Require at least one slash and a hostname-shaped prefix (contains a
		// dot before the first slash) to distinguish github.com/user/repo.git
		// from a bare local name like repo.git.
		slashIdx := strings.Index(s, "/")
		if slashIdx > 0 && strings.Contains(s[:slashIdx], ".") {
			return true // github.com/user/repo.git shape
		}
		return false // bare name (repo.git) or no hostname
	}

	return false
}
