// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// hmacTokenHashPrefix discriminates HMAC-hashed token rows from the
// legacy bare-SHA-256 rows. Stored values are "hmac:<hex>" for HMAC
// rows and bare "<hex>" for legacy rows.
//
// CA-220 (X-L5): the legacy bare-SHA-256 hash is offline-brute-forceable
// if the DB is exfiltrated (`echo ca_<guess> | sha256sum`). HMAC-SHA256
// keyed with the installation encryption key removes that capability —
// an attacker who exfiltrates the DB but not the key still cannot
// invert the stored hash.
const hmacTokenHashPrefix = "hmac:"

// legacyHashToken returns the bare hex SHA-256 hash of rawToken. Kept
// only for read-back compatibility with existing rows; new writes go
// through hmacHashToken.
func legacyHashToken(rawToken string) string {
	hash := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(hash[:])
}

// hmacHashToken returns "hmac:<hex>" where <hex> is the HMAC-SHA256 of
// rawToken keyed with key. An empty key falls back to legacyHashToken
// so installs without an encryption key configured do not silently
// break — they're explicitly opted into the SHA-256-only legacy path
// via the warn log at boot.
func hmacHashToken(rawToken string, key []byte) string {
	if len(key) == 0 {
		return legacyHashToken(rawToken)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(rawToken))
	return hmacTokenHashPrefix + hex.EncodeToString(mac.Sum(nil))
}

// isLegacyTokenHash reports whether a stored token_hash value is the
// bare-SHA-256 legacy format (no "hmac:" prefix). Used by store impls
// to decide whether to opportunistically migrate the row on a
// successful legacy-format match.
func isLegacyTokenHash(stored string) bool {
	return !strings.HasPrefix(stored, hmacTokenHashPrefix)
}

// constantTimeHashEqual compares two stored-format token hash strings
// in constant time so a hash-equality check can't leak timing.
// Currently used only by tests; store impls compare via DB lookup.
func constantTimeHashEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
