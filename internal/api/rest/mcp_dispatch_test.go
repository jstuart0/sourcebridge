// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"testing"
	"time"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// TestDispatchMapCoversBaseTools asserts that every tool name returned by
// h.baseTools() has a corresponding entry in h.toolDispatch. This is the
// inverse direction of TestRegistry_AllMCPToolsExistInBaseTools (which checks
// capability→baseTools); this test checks baseTools→dispatch.
//
// Without this guard, a phase can ship a tool definition that silently routes
// to "Unknown tool" at call time because its dispatch entry was forgotten.
// Both directions must stay green after every phase commit.
//
// Note: record_change is conditionally registered (only when changeDispatcher
// is wired). This harness does not wire changeDispatcher, so record_change
// will be absent from toolDispatch — it is therefore excluded from the
// baseTools() listing as well (recordChangeToolDefIfAvailable returns nil
// when changeDispatcher is nil). The test relies on that invariant: if the
// tool appears in baseTools(), it must be in the dispatch map.
func TestDispatchMapCoversBaseTools(t *testing.T) {
	store := graphstore.NewStore()
	ks := newMockKnowledgeStore()

	h := newMCPHandler(store, ks, nil, "", 1*time.Hour, 30*time.Second, 100, nil)

	tools := h.baseTools()

	for _, tool := range tools {
		if _, ok := h.toolDispatch[tool.Name]; !ok {
			t.Errorf("tool %q is in baseTools() but has no entry in toolDispatch — add it to registerCoreTools (or the appropriate register*Tools function)", tool.Name)
		}
	}

	if t.Failed() {
		t.Logf("toolDispatch has %d entries; baseTools() has %d entries", len(h.toolDispatch), len(tools))
	}
}
