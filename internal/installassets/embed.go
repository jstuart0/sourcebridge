// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package installassets embeds the SourceBridge installer script and exposes
// it via an HTTP handler so each SourceBridge deployment can serve
// `GET /install.sh` from its own origin.
//
// The script's source of truth lives at install.sh in this package directory.
// scripts/install.sh in the repo root is a symlink pointing here, kept so the
// script is discoverable at the repo-conventional path.
package installassets

import (
	_ "embed"
	"net/http"
)

//go:embed install.sh
var installScript []byte

// Script returns the embedded installer bytes. Used by tests and any
// caller that wants the raw bytes (not just an HTTP handler).
func Script() []byte {
	return installScript
}

// Handler returns an HTTP handler that serves the embedded installer
// script. The handler:
//   - sets Content-Type: text/x-shellscript so curl-piped-to-sh works
//     out of the box;
//   - sets X-Content-Type-Options: nosniff to prevent middleboxes from
//     reinterpreting the body;
//   - sets a small Cache-Control max-age so a frequent install-script
//     fix can roll out within minutes.
//
// The script is identical regardless of which SourceBridge install serves
// it; the version-specific binary tarballs come from the upstream GitHub
// release. A fork can still hand users the same install URL.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(installScript)
	}
}
