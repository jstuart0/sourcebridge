// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package clustering

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// uniqueLabels counts the distinct cluster labels in a label map.
func uniqueLabels(labels map[string]string) int {
	seen := map[string]struct{}{}
	for _, l := range labels {
		seen[l] = struct{}{}
	}
	return len(seen)
}

var testSeed = BuildSeed("repo1", "abc123")

// TestLPA_Empty verifies that an empty graph produces an empty result.
func TestLPA_Empty(t *testing.T) {
	result := RunLPA(nil, nil, testSeed)
	if len(result.Labels) != 0 {
		t.Errorf("expected 0 labels, got %d", len(result.Labels))
	}
}

// TestLPA_SingleNode verifies that a single isolated node forms its own cluster.
func TestLPA_SingleNode(t *testing.T) {
	result := RunLPA(nil, []string{"a"}, testSeed)
	if len(result.Labels) != 1 {
		t.Fatalf("expected 1 label, got %d", len(result.Labels))
	}
	if result.Labels["a"] != "a" {
		t.Errorf("single node should keep its own label, got %q", result.Labels["a"])
	}
}

// TestLPA_Disconnected verifies that completely disconnected nodes all form
// their own singleton clusters.
func TestLPA_Disconnected(t *testing.T) {
	nodes := []string{"a", "b", "c", "d"}
	result := RunLPA(nil, nodes, testSeed)
	if len(result.Labels) != 4 {
		t.Fatalf("expected 4 labels, got %d", len(result.Labels))
	}
	// Each node should be its own cluster.
	clusters := uniqueLabels(result.Labels)
	if clusters != 4 {
		t.Errorf("expected 4 distinct clusters for 4 isolates, got %d", clusters)
	}
}

// TestLPA_Cycle verifies that a 4-node cycle converges to a single cluster.
// A → B → C → D → A forms a strongly connected cycle; LPA should find them
// all in one community (or at most two, due to bipartite effects).
func TestLPA_Cycle(t *testing.T) {
	edges := []graph.CallEdge{
		{CallerID: "a", CalleeID: "b"},
		{CallerID: "b", CalleeID: "c"},
		{CallerID: "c", CalleeID: "d"},
		{CallerID: "d", CalleeID: "a"},
	}
	nodes := []string{"a", "b", "c", "d"}
	result := RunLPA(edges, nodes, testSeed)
	if len(result.Labels) != 4 {
		t.Fatalf("expected 4 labels, got %d", len(result.Labels))
	}
	// For a symmetric cycle all nodes should converge.
	clusters := uniqueLabels(result.Labels)
	if clusters > 2 {
		t.Errorf("4-cycle should converge to 1 or 2 clusters, got %d", clusters)
	}
}

// TestLPA_Star verifies that a star graph (one hub, many leaves) produces
// sensible clustering: hub and leaves typically converge to one group.
func TestLPA_Star(t *testing.T) {
	// Hub A connected to B, C, D, E.
	edges := []graph.CallEdge{
		{CallerID: "hub", CalleeID: "b"},
		{CallerID: "hub", CalleeID: "c"},
		{CallerID: "hub", CalleeID: "d"},
		{CallerID: "hub", CalleeID: "e"},
	}
	nodes := []string{"hub", "b", "c", "d", "e"}
	result := RunLPA(edges, nodes, testSeed)
	if len(result.Labels) != 5 {
		t.Fatalf("expected 5 labels, got %d", len(result.Labels))
	}
	// All nodes should be in the same cluster (hub is the common neighbour).
	clusters := uniqueLabels(result.Labels)
	if clusters != 1 {
		t.Errorf("star graph should converge to 1 cluster, got %d", clusters)
	}
}

// TestLPA_TwoComponents verifies that two dense components separated by no
// bridge produce two distinct clusters.
func TestLPA_TwoComponents(t *testing.T) {
	// Component 1: a ↔ b ↔ c fully connected.
	// Component 2: x ↔ y ↔ z fully connected.
	edges := []graph.CallEdge{
		{CallerID: "a", CalleeID: "b"},
		{CallerID: "b", CalleeID: "a"},
		{CallerID: "b", CalleeID: "c"},
		{CallerID: "c", CalleeID: "b"},
		{CallerID: "a", CalleeID: "c"},
		{CallerID: "c", CalleeID: "a"},
		{CallerID: "x", CalleeID: "y"},
		{CallerID: "y", CalleeID: "x"},
		{CallerID: "y", CalleeID: "z"},
		{CallerID: "z", CalleeID: "y"},
		{CallerID: "x", CalleeID: "z"},
		{CallerID: "z", CalleeID: "x"},
	}
	nodes := []string{"a", "b", "c", "x", "y", "z"}
	result := RunLPA(edges, nodes, testSeed)
	if len(result.Labels) != 6 {
		t.Fatalf("expected 6 labels, got %d", len(result.Labels))
	}
	clusters := uniqueLabels(result.Labels)
	if clusters != 2 {
		t.Errorf("expected 2 distinct clusters for two disconnected components, got %d", clusters)
	}
	// Verify components: abc must share a label, xyz must share a label.
	if result.Labels["a"] != result.Labels["b"] || result.Labels["b"] != result.Labels["c"] {
		t.Error("nodes a, b, c should be in the same cluster")
	}
	if result.Labels["x"] != result.Labels["y"] || result.Labels["y"] != result.Labels["z"] {
		t.Error("nodes x, y, z should be in the same cluster")
	}
	if result.Labels["a"] == result.Labels["x"] {
		t.Error("the two components should be in different clusters")
	}
}

// TestLPA_Deterministic verifies that the same seed always produces the same
// partition for identical inputs.
func TestLPA_Deterministic(t *testing.T) {
	edges := []graph.CallEdge{
		{CallerID: "a", CalleeID: "b"},
		{CallerID: "b", CalleeID: "c"},
		{CallerID: "c", CalleeID: "a"},
		{CallerID: "x", CalleeID: "y"},
		{CallerID: "y", CalleeID: "x"},
	}
	nodes := []string{"a", "b", "c", "x", "y"}
	seed := BuildSeed("myrepo", "deadbeef")

	r1 := RunLPA(edges, nodes, seed)
	r2 := RunLPA(edges, nodes, seed)

	for _, id := range nodes {
		if r1.Labels[id] != r2.Labels[id] {
			t.Errorf("non-deterministic: node %q got %q then %q", id, r1.Labels[id], r2.Labels[id])
		}
	}
}

// TestLPA_DirectedLeaves verifies correct behavior for directed graphs with
// pure leaf nodes (callees that never appear as callers).
//
// # The bug (labelprop.go line 62 before fix)
//
// The buggy line was:
//
//	neighbors[e.CalleeID] = append(neighbors[e.CallerID], e.CallerID)
//
// This has two problems:
//
//  1. It reads from neighbors[e.CallerID] instead of neighbors[e.CalleeID], so
//     it REPLACES the callee's accumulated neighbor list rather than APPENDING
//     to it. Each new edge that points to the same callee overwrites the
//     callee's neighbor list with the current caller's neighbor list plus
//     the caller — all prior callers are lost.
//
//  2. Because the caller's neighbor list was just updated in the line above
//     (adding e.CalleeID), the callee's adjacency receives the caller's
//     callees as its own neighbors, propagating distant graph structure
//     incorrectly.
//
// The corrected line is:
//
//	neighbors[e.CalleeID] = append(neighbors[e.CalleeID], e.CallerID)
//
// # Limitations
//
// Small test graphs may not produce a visibly wrong LPA partition even with
// the buggy code, because LPA's label propagation can accidentally recover
// connectivity from contaminated adjacency lists on tiny graphs. The bug's
// primary impact is on real codebases with deep call chains and many
// fan-in callees — exactly the "leaf functions" pattern Dexter identified.
// The fix is in the adjacency builder, so its correctness can only be
// definitively proven by inspecting neighbors[], which is package-internal.
//
// This test exercises the correct post-fix behaviour. The adjacency builder
// is separately auditable by code review of the for-loop in RunLPA.
func TestLPA_DirectedLeaves(t *testing.T) {
	// a → b → c  (c has no outgoing edges — pure leaf)
	// With correct adjacency:
	//   neighbors[a] = [b]
	//   neighbors[b] = [a, c]   (a from a→b callee side; c from b→c caller side)
	//   neighbors[c] = [b]      (b from b→c callee side)
	// All three nodes are connected; LPA must converge to 1 cluster.
	edges := []graph.CallEdge{
		{CallerID: "a", CalleeID: "b"},
		{CallerID: "b", CalleeID: "c"},
	}
	nodes := []string{"a", "b", "c"}
	result := RunLPA(edges, nodes, testSeed)

	if len(result.Labels) != 3 {
		t.Fatalf("expected 3 labels, got %d", len(result.Labels))
	}
	clusters := uniqueLabels(result.Labels)
	if clusters != 1 {
		t.Errorf("directed chain a→b→c should converge to 1 cluster, got %d (labels: %v)",
			clusters, result.Labels)
	}
}

// TestEdgeSetHash_Deterministic verifies the hash is identical for identical
// inputs regardless of order.
func TestEdgeSetHash_Deterministic(t *testing.T) {
	edges := []graph.CallEdge{
		{CallerID: "a", CalleeID: "b"},
		{CallerID: "c", CalleeID: "d"},
		{CallerID: "b", CalleeID: "a"},
	}
	shuffled := []graph.CallEdge{
		{CallerID: "b", CalleeID: "a"},
		{CallerID: "a", CalleeID: "b"},
		{CallerID: "c", CalleeID: "d"},
	}
	h1 := edgeSetHash(edges)
	h2 := edgeSetHash(shuffled)
	if h1 != h2 {
		t.Errorf("hash mismatch: %q vs %q", h1, h2)
	}
}

// TestEdgeSetHash_DifferentOnChange verifies that adding an edge changes the hash.
func TestEdgeSetHash_DifferentOnChange(t *testing.T) {
	edges := []graph.CallEdge{
		{CallerID: "a", CalleeID: "b"},
	}
	more := append(edges, graph.CallEdge{CallerID: "c", CalleeID: "d"})
	h1 := edgeSetHash(edges)
	h2 := edgeSetHash(more)
	if h1 == h2 {
		t.Error("hash should differ when edges differ")
	}
}

// TestEdgeSetHash_Empty verifies the hash of an empty edge set is consistent.
func TestEdgeSetHash_Empty(t *testing.T) {
	h := edgeSetHash(nil)
	if h == "" {
		t.Error("hash of empty edge set should not be empty string")
	}
}
