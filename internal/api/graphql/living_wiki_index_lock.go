// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package graphql — index-page write serialization for the Living Wiki feature.
//
// Why package-level: two parallel cold-start jobs (Overview + Detailed, per
// LD-12) build independent runner closures via buildColdStartRunner, so a
// closure-local mutex would NOT serialize across them. Cross-pod races still
// resolve through Confluence's UpsertPage optimistic-version retry — but we
// do NOT rely on that as the primary serialization; within a process, a
// deterministic mutex is required to avoid last-writer-wins flicker.
//
// The map grows monotonically over the process lifetime. Entries are cleaned
// up via RemoveIndexMutex, which is called from the repository-deletion path.
// If no deletion handler exists (as in the OSS build today), the slow leak is
// bounded: one *sync.Mutex (~24 bytes) per repo ID ever seen in this process.
// For 10 K repos that is ~240 KB — acceptable. The file-level comment ensures
// future cleanup work catches this.
package graphql

import "sync"

// indexWriteMutexes holds one *sync.Mutex per repo ID, used to serialize
// writes to the combined Living Wiki index page (<repoID>.__index__)
// across all cold-start jobs running in this process.
var indexWriteMutexes sync.Map // map[repoID string]*sync.Mutex

// indexMutexFor returns the *sync.Mutex for repoID, creating one on first
// access. The map entry is never nil: LoadOrStore atomically creates exactly
// one *sync.Mutex per key and returns it to every concurrent caller.
func indexMutexFor(repoID string) *sync.Mutex {
	m, _ := indexWriteMutexes.LoadOrStore(repoID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// RemoveIndexMutex drops the mutex entry for repoID. Call this from the
// repository-deletion handler (deleteRepository resolver) so the map does not
// grow without bound. Safe to call when no entry exists (idempotent).
func RemoveIndexMutex(repoID string) {
	indexWriteMutexes.Delete(repoID)
}
