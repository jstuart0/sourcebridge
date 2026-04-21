// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// Captures slog output for assertion. Every test installs and restores the
// default logger via this helper.
type logCapture struct {
	buf *bytes.Buffer
	old *slog.Logger
}

func captureSlog(t *testing.T) *logCapture {
	t.Helper()
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	old := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logCapture{buf: buf, old: old}
}

func (lc *logCapture) text() string { return lc.buf.String() }
func (lc *logCapture) contains(s string) bool {
	return strings.Contains(lc.buf.String(), s)
}

// seedStaleArtifact creates a ready+stale artifact in the given mode.
func seedStaleArtifact(
	t *testing.T,
	store *knowledgepkg.MemStore,
	repoID string,
	typ knowledgepkg.ArtifactType,
	depth knowledgepkg.Depth,
	mode knowledgepkg.GenerationMode,
) *knowledgepkg.Artifact {
	t.Helper()
	a, err := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		RepositoryID:   repoID,
		Type:           typ,
		Audience:       knowledgepkg.AudienceDeveloper,
		Depth:          depth,
		GenerationMode: mode,
		Status:         knowledgepkg.StatusPending,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", typ, err)
	}
	if err := store.UpdateKnowledgeArtifactStatus(a.ID, knowledgepkg.StatusReady); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := store.MarkKnowledgeArtifactStale(a.ID, true); err != nil {
		t.Fatalf("stale: %v", err)
	}
	return store.GetKnowledgeArtifact(a.ID)
}

// resolverWithStore returns a mutationResolver wired up enough to exercise
// the auto-regen driver. It intentionally doesn't attach a Worker — tests
// only verify driver decisions, not downstream regen flow (live mode's
// RefreshKnowledgeArtifact call path is covered by its own resolver tests).
func resolverWithStore(store knowledgepkg.KnowledgeStore) *mutationResolver {
	return &mutationResolver{&Resolver{KnowledgeStore: store}}
}

// resetRegenRateLimiter wipes the global rate-limit windows between tests so
// one test's attempts don't bleed into another.
func resetRegenRateLimiter() {
	l := deltaRegenRateLimiter()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.windows = make(map[string][]time.Time)
}

func TestEnqueueStaleArtifactRefresh_ModeOff_NoOp(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "off")
	resetRegenRateLimiter()
	lc := captureSlog(t)

	store := knowledgepkg.NewMemStore()
	a := seedStaleArtifact(t, store, "repo-1", knowledgepkg.ArtifactCliffNotes, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	r := resolverWithStore(store)

	r.enqueueStaleArtifactRefresh("repo-1", []string{a.ID}, "report-off")

	if lc.contains("delta_regen_decision") || lc.contains("delta_regen_shadow_would_enqueue") {
		t.Fatalf("mode=off should short-circuit; logs: %s", lc.text())
	}
}

func TestEnqueueStaleArtifactRefresh_ShadowMode_LogsButDoesNotEnqueue(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "shadow")
	resetRegenRateLimiter()
	lc := captureSlog(t)

	store := knowledgepkg.NewMemStore()
	a := seedStaleArtifact(t, store, "repo-1", knowledgepkg.ArtifactCliffNotes, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	r := resolverWithStore(store)

	r.enqueueStaleArtifactRefresh("repo-1", []string{a.ID}, "report-shadow")

	if !lc.contains("delta_regen_decision") {
		t.Fatalf("expected decision log, got: %s", lc.text())
	}
	if !lc.contains("delta_regen_shadow_would_enqueue") {
		t.Fatalf("expected shadow-would-enqueue log, got: %s", lc.text())
	}
	if !lc.contains("\"shadow\"") && !lc.contains("mode=shadow") {
		t.Fatalf("expected mode=shadow in logs, got: %s", lc.text())
	}
	// Artifact must remain stale under shadow — we never actually refreshed.
	after := store.GetKnowledgeArtifact(a.ID)
	if !after.Stale {
		t.Fatalf("shadow mode should not unset stale; got stale=%v", after.Stale)
	}
}

func TestEnqueueStaleArtifactRefresh_RequiresPhase1(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "false")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "live")
	resetRegenRateLimiter()
	lc := captureSlog(t)

	store := knowledgepkg.NewMemStore()
	a := seedStaleArtifact(t, store, "repo-1", knowledgepkg.ArtifactCliffNotes, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	r := resolverWithStore(store)

	r.enqueueStaleArtifactRefresh("repo-1", []string{a.ID}, "report-no-p1")

	if lc.contains("delta_regen_decision") {
		t.Fatalf("Phase 1 off should collapse mode to off; logs: %s", lc.text())
	}
}

func TestEnqueueStaleArtifactRefresh_RespectsCap(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "shadow")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MAX_PER_INDEX", "3")
	resetRegenRateLimiter()
	lc := captureSlog(t)

	store := knowledgepkg.NewMemStore()
	var ids []string
	for i := 0; i < 10; i++ {
		a := seedStaleArtifact(t, store, "repo-cap", knowledgepkg.ArtifactCliffNotes, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
		ids = append(ids, a.ID)
	}
	r := resolverWithStore(store)
	r.enqueueStaleArtifactRefresh("repo-cap", ids, "report-cap")

	// Only 3 shadow-enqueue lines, 7 over cap.
	n := strings.Count(lc.text(), "delta_regen_shadow_would_enqueue")
	if n != 3 {
		t.Fatalf("expected 3 shadow lines (cap=3), got %d\n%s", n, lc.text())
	}
	if !strings.Contains(lc.text(), "over_cap=7") {
		t.Fatalf("expected over_cap=7 in decision log: %s", lc.text())
	}
}

func TestEnqueueStaleArtifactRefresh_Prioritization(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "shadow")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MAX_PER_INDEX", "5")
	resetRegenRateLimiter()
	lc := captureSlog(t)

	store := knowledgepkg.NewMemStore()
	// Mixed types, seeded in the reverse of priority order.
	story := seedStaleArtifact(t, store, "repo-p", knowledgepkg.ArtifactWorkflowStory, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	tour := seedStaleArtifact(t, store, "repo-p", knowledgepkg.ArtifactCodeTour, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	learn := seedStaleArtifact(t, store, "repo-p", knowledgepkg.ArtifactLearningPath, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	cliff := seedStaleArtifact(t, store, "repo-p", knowledgepkg.ArtifactCliffNotes, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	r := resolverWithStore(store)
	r.enqueueStaleArtifactRefresh("repo-p", []string{story.ID, tour.ID, learn.ID, cliff.ID}, "report-p")

	// Find order of shadow lines: cliff → learning → code_tour → workflow.
	// slog.Info stringifies ArtifactType values as their underlying lowercase
	// string constants ("cliff_notes", "learning_path", etc.).
	text := lc.text()
	idxCliff := strings.Index(text, "type=cliff_notes")
	idxLearn := strings.Index(text, "type=learning_path")
	idxTour := strings.Index(text, "type=code_tour")
	idxStory := strings.Index(text, "type=workflow_story")
	if idxCliff < 0 || idxLearn < 0 || idxTour < 0 || idxStory < 0 {
		t.Fatalf("missing one of the types in logs:\n%s", text)
	}
	if !(idxCliff < idxLearn && idxLearn < idxTour && idxTour < idxStory) {
		t.Fatalf("priority order wrong: cliff=%d learn=%d tour=%d story=%d", idxCliff, idxLearn, idxTour, idxStory)
	}
}

func TestEnqueueStaleArtifactRefresh_SkipsNonStaleAndNonReady(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "shadow")
	resetRegenRateLimiter()
	lc := captureSlog(t)

	store := knowledgepkg.NewMemStore()
	// Create three artifacts: one stale+ready, one non-stale+ready,
	// one stale+generating. Only the first should be considered.
	good := seedStaleArtifact(t, store, "repo-s", knowledgepkg.ArtifactCliffNotes, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	noStale, _ := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		RepositoryID: "repo-s", Type: knowledgepkg.ArtifactLearningPath,
		Audience: knowledgepkg.AudienceDeveloper, Depth: knowledgepkg.DepthMedium,
		GenerationMode: knowledgepkg.GenerationModeClassic, Status: knowledgepkg.StatusPending,
	})
	_ = store.UpdateKnowledgeArtifactStatus(noStale.ID, knowledgepkg.StatusReady)
	inflight := seedStaleArtifact(t, store, "repo-s", knowledgepkg.ArtifactCodeTour, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	_ = store.UpdateKnowledgeArtifactStatus(inflight.ID, knowledgepkg.StatusGenerating)
	r := resolverWithStore(store)
	r.enqueueStaleArtifactRefresh("repo-s", []string{good.ID, noStale.ID, inflight.ID}, "report-s")

	n := strings.Count(lc.text(), "delta_regen_shadow_would_enqueue")
	if n != 1 {
		t.Fatalf("expected exactly 1 candidate after filtering, got %d\n%s", n, lc.text())
	}
	if !strings.Contains(lc.text(), good.ID) {
		t.Fatalf("expected the stale+ready artifact (%s) in logs:\n%s", good.ID, lc.text())
	}
}

func TestEnqueueStaleArtifactRefresh_DefersUnderstandingFirstUntilFreshUnderstanding(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "shadow")
	resetRegenRateLimiter()
	lc := captureSlog(t)

	store := knowledgepkg.NewMemStore()
	// Seed a classic artifact and an understanding_first artifact. No
	// repository understanding record exists yet (simulates missing /
	// needs-refresh).
	classicArt := seedStaleArtifact(t, store, "repo-u", knowledgepkg.ArtifactCliffNotes, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	ufArt := seedStaleArtifact(t, store, "repo-u", knowledgepkg.ArtifactLearningPath, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeUnderstandingFirst)
	r := resolverWithStore(store)
	r.enqueueStaleArtifactRefresh("repo-u", []string{classicArt.ID, ufArt.ID}, "report-u")

	text := lc.text()
	// Classic artifact should have been enqueued; understanding-first should be deferred.
	if !strings.Contains(text, classicArt.ID) {
		t.Fatalf("expected classic artifact enqueued: %s", text)
	}
	if strings.Contains(text, "delta_regen_shadow_would_enqueue") &&
		strings.Contains(text, "artifact_id="+ufArt.ID) &&
		strings.Contains(text, "type=LEARNING_PATH") &&
		!strings.Contains(text, "delta_regen_deferred") {
		t.Fatalf("understanding_first artifact should be deferred, not enqueued: %s", text)
	}
	if !strings.Contains(text, "delta_regen_deferred") {
		t.Fatalf("expected delta_regen_deferred log for ungated understanding: %s", text)
	}
	if !strings.Contains(text, "understanding_not_fresh") {
		t.Fatalf("expected deferral reason: %s", text)
	}

	// Now seed a fresh understanding and rerun — ufArt should not be deferred.
	_, _ = store.StoreRepositoryUnderstanding(&knowledgepkg.RepositoryUnderstanding{
		RepositoryID: "repo-u",
		Scope:        (&knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}).NormalizePtr(),
		Stage:        knowledgepkg.UnderstandingReady,
		RevisionFP:   "rev-1",
	})
	// Re-stale the artifacts (first pass unset them via the decision flow).
	_ = store.MarkKnowledgeArtifactStale(ufArt.ID, true)

	lc2 := captureSlog(t)
	resetRegenRateLimiter()
	r.enqueueStaleArtifactRefresh("repo-u", []string{ufArt.ID}, "report-u2")
	if !strings.Contains(lc2.text(), "delta_regen_shadow_would_enqueue") {
		t.Fatalf("expected understanding_first artifact to be enqueued with fresh understanding: %s", lc2.text())
	}
	if strings.Contains(lc2.text(), "delta_regen_deferred") {
		t.Fatalf("should NOT defer with fresh understanding: %s", lc2.text())
	}
}

func TestEnqueueStaleArtifactRefresh_RateLimit(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "shadow")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MAX_PER_REPO_PER_HOUR", "3")
	resetRegenRateLimiter()
	lc := captureSlog(t)

	store := knowledgepkg.NewMemStore()
	a := seedStaleArtifact(t, store, "repo-r", knowledgepkg.ArtifactCliffNotes, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
	r := resolverWithStore(store)

	// 3 attempts fit under the cap.
	for i := 0; i < 3; i++ {
		_ = store.MarkKnowledgeArtifactStale(a.ID, true)
		r.enqueueStaleArtifactRefresh("repo-r", []string{a.ID}, "report-r")
	}
	// 4th should hit the rate limit.
	_ = store.MarkKnowledgeArtifactStale(a.ID, true)
	r.enqueueStaleArtifactRefresh("repo-r", []string{a.ID}, "report-r-4")

	if !strings.Contains(lc.text(), "delta_regen_rate_limited") {
		t.Fatalf("expected rate-limit log, got: %s", lc.text())
	}
	if strings.Count(lc.text(), "delta_regen_decision") != 3 {
		t.Fatalf("expected exactly 3 decision logs (4th rate-limited), got %d\n%s",
			strings.Count(lc.text(), "delta_regen_decision"), lc.text())
	}
}

func TestEnqueueStaleArtifactRefresh_ShadowVsLiveDecisionParity(t *testing.T) {
	// Same candidate shape run in shadow and live produces the same
	// selection shape: same count, same priority order. We can't compare
	// raw artifact IDs across runs (MemStore mints fresh UUIDs), so we
	// map each selected ID back to its artifact type and compare the type
	// sequence.
	runSelectedTypes := func(mode string) []knowledgepkg.ArtifactType {
		t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
		t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", mode)
		t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MAX_PER_INDEX", "3")
		resetRegenRateLimiter()
		lc := captureSlog(t)

		store := knowledgepkg.NewMemStore()
		idToType := make(map[string]knowledgepkg.ArtifactType)
		var ids []string
		types := []knowledgepkg.ArtifactType{
			knowledgepkg.ArtifactWorkflowStory,
			knowledgepkg.ArtifactCliffNotes,
			knowledgepkg.ArtifactLearningPath,
			knowledgepkg.ArtifactCodeTour,
		}
		for _, ty := range types {
			a := seedStaleArtifact(t, store, "repo-parity", ty, knowledgepkg.DepthMedium, knowledgepkg.GenerationModeClassic)
			ids = append(ids, a.ID)
			idToType[a.ID] = ty
		}
		r := resolverWithStore(store)
		r.enqueueStaleArtifactRefresh("repo-parity", ids, "report-parity")

		// Parse selected IDs from the delta_regen_decision line. This line
		// fires synchronously in both modes before any goroutine spawns.
		text := lc.text()
		var selected []string
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "delta_regen_decision") {
				continue
			}
			tag := "artifact_ids=\"["
			i := strings.Index(line, tag)
			if i < 0 {
				continue
			}
			rest := line[i+len(tag):]
			end := strings.Index(rest, "]\"")
			if end < 0 {
				continue
			}
			selected = strings.Fields(rest[:end])
			break
		}
		out := make([]knowledgepkg.ArtifactType, 0, len(selected))
		for _, id := range selected {
			if ty, ok := idToType[id]; ok {
				out = append(out, ty)
			}
		}
		return out
	}

	shadow := runSelectedTypes("shadow")
	live := runSelectedTypes("live")
	if len(shadow) == 0 || len(shadow) != len(live) {
		t.Fatalf("expected equal non-empty selection sets; shadow=%v live=%v", shadow, live)
	}
	for i := range shadow {
		if shadow[i] != live[i] {
			t.Fatalf("priority parity failed at index %d: shadow=%q live=%q", i, shadow[i], live[i])
		}
	}
}
