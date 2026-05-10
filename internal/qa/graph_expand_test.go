// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"testing"
)

type fakeStore struct {
	callers map[string][]string
	callees map[string][]string
}

func (f *fakeStore) GetCallers(_ context.Context, id string) []string { return f.callers[id] }
func (f *fakeStore) GetCallees(_ context.Context, id string) []string { return f.callees[id] }

type fakeLookup map[string]GraphNeighbor

func (f fakeLookup) Lookup(_ context.Context, id string) (string, string, string, int, int, bool) {
	n, ok := f[id]
	if !ok {
		return "", "", "", 0, 0, false
	}
	return n.QualifiedName, n.FilePath, n.Language, n.StartLine, n.EndLine, true
}

func TestGraphExpander_Callers(t *testing.T) {
	store := &fakeStore{
		callers: map[string][]string{"focal": {"caller-a", "caller-b", "unknown"}},
	}
	lookup := fakeLookup{
		"caller-a": {QualifiedName: "pkg.A", FilePath: "a.go", StartLine: 10, EndLine: 20, Language: "go"},
		"caller-b": {QualifiedName: "pkg.B", FilePath: "b.go", StartLine: 30, EndLine: 40, Language: "go"},
	}
	g := NewGraphExpander(store, lookup)
	got := g.GetCallers(context.Background(), "focal")
	if len(got) != 2 {
		t.Fatalf("expected 2 resolved neighbors (unknown skipped), got %d", len(got))
	}
	if got[0].QualifiedName != "pkg.A" || got[1].QualifiedName != "pkg.B" {
		t.Errorf("order/resolution mismatch: %+v", got)
	}
}

func TestGraphExpander_Callees(t *testing.T) {
	store := &fakeStore{
		callees: map[string][]string{"focal": {"callee"}},
	}
	lookup := fakeLookup{
		"callee": {QualifiedName: "pkg.C", FilePath: "c.go"},
	}
	g := NewGraphExpander(store, lookup)
	got := g.GetCallees(context.Background(), "focal")
	if len(got) != 1 || got[0].QualifiedName != "pkg.C" {
		t.Errorf("unexpected callees: %+v", got)
	}
}

func TestGraphExpander_NilInputs(t *testing.T) {
	var g GraphExpander = NewGraphExpander(nil, nil)
	if got := g.GetCallers(context.Background(), "x"); got != nil {
		t.Errorf("nil store should return nil, got %v", got)
	}
	if got := g.GetCallees(context.Background(), "x"); got != nil {
		t.Errorf("nil store should return nil, got %v", got)
	}
}

func TestCollectGraphNeighbors_Caps(t *testing.T) {
	ids := make([]string, 30)
	lookup := fakeLookup{}
	for i := range ids {
		ids[i] = "s" + string(rune('a'+i))
		lookup[ids[i]] = GraphNeighbor{QualifiedName: ids[i]}
	}
	store := &fakeStore{
		callers: map[string][]string{"f": ids},
		callees: map[string][]string{"f": ids},
	}
	g := NewGraphExpander(store, lookup)
	out := collectGraphNeighbors(context.Background(), g, "f", 5)
	if len(out) != 10 {
		t.Errorf("expected 5 callers + 5 callees = 10, got %d", len(out))
	}
}

func TestCollectGraphNeighbors_NoFocal(t *testing.T) {
	g := NewGraphExpander(&fakeStore{}, fakeLookup{})
	if n := collectGraphNeighbors(context.Background(), g, "", 5); n != nil {
		t.Errorf("expected nil when focal missing, got %v", n)
	}
}
