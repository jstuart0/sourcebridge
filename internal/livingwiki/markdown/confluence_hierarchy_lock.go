// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package markdown

import "sync"

// hierarchyLocks holds one *sync.Mutex per HierarchyLockKey, serializing
// concurrent ensureHierarchy calls across ConfluenceWriter instances in the
// same process. Cross-pod races are resolved separately by EnsurePage's
// create-conflict recovery (see EnsurePage in confluence_http.go).
var hierarchyLocks sync.Map // map[string]*sync.Mutex

// hierarchyLockFor returns the process-wide mutex for the given key.
//
// An empty key is the defensive last-resort path for tests that do not
// populate HierarchyLockKey. Rather than colliding every caller under a
// shared bucket, an empty key disables serialization by returning a fresh
// mutex that is not stored in hierarchyLocks. Production callers must always
// pass a non-empty key derived from (siteURL, spaceKey, repoID).
func hierarchyLockFor(key string) *sync.Mutex {
	if key == "" {
		// Fresh mutex — no cross-writer serialization for this call.
		return &sync.Mutex{}
	}
	m, _ := hierarchyLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}
