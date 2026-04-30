// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"sync"
	"testing"
)

// TestIndexMutexIsPackageLevel verifies the three properties required by M3:
//
//  1. Calling indexMutexFor("repoA") twice returns the same pointer.
//  2. Calling indexMutexFor("repoA") and indexMutexFor("repoB") returns
//     different pointers (distinct per-repo mutexes).
//  3. Two goroutines simulating parallel "Build All" jobs (each acquiring the
//     mutex for "repoA", writing to a shared slice, and releasing) do NOT
//     interleave their writes — the per-repo mutex provides cross-closure
//     serialization.
func TestIndexMutexIsPackageLevel(t *testing.T) {
	// Clean up any pre-existing entry so this test is hermetic.
	RemoveIndexMutex("lock-test-repo-A")
	RemoveIndexMutex("lock-test-repo-B")
	defer RemoveIndexMutex("lock-test-repo-A")
	defer RemoveIndexMutex("lock-test-repo-B")

	// Property 1: same key → same pointer.
	m1 := indexMutexFor("lock-test-repo-A")
	m2 := indexMutexFor("lock-test-repo-A")
	if m1 != m2 {
		t.Errorf("indexMutexFor(A) returned different pointers on two calls: %p != %p", m1, m2)
	}

	// Property 2: different keys → different pointers.
	mB := indexMutexFor("lock-test-repo-B")
	if m1 == mB {
		t.Errorf("indexMutexFor(A) and indexMutexFor(B) returned the same pointer %p", m1)
	}

	// Property 3: cross-closure serialization.
	// Two goroutines each do 50 iterations of: lock → append "goroutine N begin"
	// → append "goroutine N end" → unlock. Correct serialization means "begin"
	// and "end" from the same goroutine are always adjacent in the log.
	var mu sync.Mutex // protects log slice
	var log []int     // interleaved writes; even = begin, odd = end

	const iters = 50
	var wg sync.WaitGroup
	wg.Add(2)

	for g := 0; g < 2; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				m := indexMutexFor("lock-test-repo-A")
				m.Lock()
				mu.Lock()
				log = append(log, g*10+0) // begin token
				mu.Unlock()

				// Simulate some work without the log lock held.
				// The index-write mutex is still held; the other goroutine blocks here.
				mu.Lock()
				log = append(log, g*10+1) // end token
				mu.Unlock()

				m.Unlock()
			}
		}()
	}
	wg.Wait()

	// Verify that begin/end pairs are never interleaved.
	for i := 0; i+1 < len(log); i += 2 {
		begin, end := log[i], log[i+1]
		// begin token: g*10+0, end token: g*10+1 → (end - begin) must be 1.
		if end-begin != 1 {
			t.Errorf("interleaved writes at log[%d..%d]: begin=%d end=%d (diff %d; want 1)",
				i, i+1, begin, end, end-begin)
			break
		}
	}
}

// TestRemoveIndexMutexIsIdempotent verifies that removing a non-existent entry
// does not panic and that a subsequent indexMutexFor call creates a fresh mutex.
func TestRemoveIndexMutexIsIdempotent(t *testing.T) {
	// Remove something that was never inserted.
	RemoveIndexMutex("never-seen-repo")
	RemoveIndexMutex("never-seen-repo") // second call must not panic

	// After removal, indexMutexFor should create a brand-new entry.
	defer RemoveIndexMutex("fresh-after-remove")
	m1 := indexMutexFor("fresh-after-remove")
	RemoveIndexMutex("fresh-after-remove")
	m2 := indexMutexFor("fresh-after-remove")
	if m1 == m2 {
		t.Errorf("expected fresh mutex after RemoveIndexMutex; got same pointer %p", m1)
	}
}
