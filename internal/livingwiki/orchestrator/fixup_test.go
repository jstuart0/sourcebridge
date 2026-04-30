// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	livingwiki "github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ─── Stub implementations ────────────────────────────────────────────────────

// fakeStatusStore is a minimal in-memory PagePublishStatusStore for fixup tests.
type fakeStatusStore struct {
	mu   sync.Mutex
	rows []livingwiki.PagePublishStatusRow
	// updateFixupCalls records args passed to UpdateFixupStatus.
	updateFixupCalls []livingwiki.UpdateFixupStatusArgs
}

func (s *fakeStatusStore) SetReady(_ context.Context, _ livingwiki.SetReadyArgs) error {
	return nil
}

func (s *fakeStatusStore) SetNonReady(_ context.Context, _ livingwiki.SetNonReadyArgs) error {
	return nil
}

func (s *fakeStatusStore) ListByRepo(_ context.Context, repoID string) ([]livingwiki.PagePublishStatusRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []livingwiki.PagePublishStatusRow
	for _, r := range s.rows {
		if r.RepoID == repoID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStatusStore) LoadFingerprints(_ context.Context, repoID string) (map[string]map[string]livingwiki.PagePublishStatusRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// outer key: pageID; inner key: sinkKind+"/"+integrationName
	out := make(map[string]map[string]livingwiki.PagePublishStatusRow)
	for _, r := range s.rows {
		if r.RepoID != repoID {
			continue
		}
		if out[r.PageID] == nil {
			out[r.PageID] = make(map[string]livingwiki.PagePublishStatusRow)
		}
		key := r.SinkKind + "/" + r.IntegrationName
		out[r.PageID][key] = r
	}
	return out, nil
}

func (s *fakeStatusStore) UpdateFixupStatus(_ context.Context, args livingwiki.UpdateFixupStatusArgs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateFixupCalls = append(s.updateFixupCalls, args)
	// Update matching rows in-place so subsequent ListByRepo reflects the change.
	for i, r := range s.rows {
		if r.RepoID == args.RepoID && r.PageID == args.PageID &&
			r.SinkKind == args.SinkKind && r.IntegrationName == args.IntegrationName {
			s.rows[i].FixupStatus = args.FixupStatus
			s.rows[i].HasStubs = args.HasStubs
		}
	}
	return nil
}

// fakeSinkWriter is a SinkWriter that records calls without doing real I/O.
type fakeSinkWriter struct {
	mu    sync.Mutex
	pages []ast.Page
	kind  markdown.SinkKind
}

func (f *fakeSinkWriter) Kind() markdown.SinkKind { return f.kind }
func (f *fakeSinkWriter) WritePage(_ context.Context, page ast.Page) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages = append(f.pages, page)
	return nil
}

// namedFakeWriter wraps fakeSinkWriter into a sinks.NamedSinkWriter.
func namedFakeWriter(name string, kind markdown.SinkKind) (sinks.NamedSinkWriter, *fakeSinkWriter) {
	w := &fakeSinkWriter{kind: kind}
	return sinks.NamedSinkWriter{Name: name, Writer: w}, w
}

// makeArchPageFixup makes a minimal ast.Page with a freeform "Related pages"
// block containing stub XHTML, and StubTargetPageIDs set.
func makeArchPageFixup(pageID string, stubTargets []string) ast.Page {
	blockID := ast.GenerateBlockID(pageID, "Related pages", ast.BlockKindFreeform, 0)
	return ast.Page{
		ID: pageID,
		Blocks: []ast.Block{
			{
				ID:   blockID,
				Kind: ast.BlockKindFreeform,
				Content: ast.BlockContent{
					Freeform: &ast.FreeformContent{
						Raw: `<ac:structured-macro ac:name="info"><ac:rich-text-body><p>stub</p></ac:rich-text-body></ac:structured-macro>`,
					},
				},
			},
		},
		StubTargetPageIDs: stubTargets,
	}
}

// genInputFor returns a minimal GenerateInput for the given repoID.
func genInputFor(repoID string) templates.GenerateInput {
	return templates.GenerateInput{RepoID: repoID}
}

const (
	fixupTestRepoID = "repo-fixup"
	fixupTestPRID   = "pr-fixup-1"
	fixupTestSink   = "CONFLUENCE"
	fixupTestInteg  = "eng-docs"
)

// ─── Tests ───────────────────────────────────────────────────────────────────

// TestFixupPassClearsStubsOnceTargetsLand verifies the happy-path: a page
// published with stubs is re-rendered and dispatched once its target is ready.
func TestFixupPassClearsStubsOnceTargetsLand(t *testing.T) {
	ctx := context.Background()

	store := &fakeStatusStore{}
	// Page A has fixup_status=pending; its stub target "overview.pkg-b" is ready.
	store.rows = []livingwiki.PagePublishStatusRow{
		{
			RepoID:            fixupTestRepoID,
			PageID:            "overview.pkg-a",
			SinkKind:          fixupTestSink,
			IntegrationName:   fixupTestInteg,
			FixupStatus:       livingwiki.FixupStatusPending,
			StubTargetPageIDs: []string{"overview.pkg-b"},
			Status:            "ready",
		},
		// The stub target is itself ready on all sinks.
		{
			RepoID:          fixupTestRepoID,
			PageID:          "overview.pkg-b",
			SinkKind:        fixupTestSink,
			IntegrationName: fixupTestInteg,
			FixupStatus:     livingwiki.FixupStatusNone,
			Status:          "ready",
		},
	}

	pageStore := orchestrator.NewMemoryPageStore()
	_ = pageStore.SetProposed(ctx, fixupTestRepoID, fixupTestPRID, makeArchPageFixup("overview.pkg-a", []string{"overview.pkg-b"}))

	nsw, _ := namedFakeWriter(fixupTestInteg, markdown.SinkKindConfluence)
	writers := []sinks.NamedSinkWriter{nsw}

	pr := orchestrator.NewMemoryWikiPR(fixupTestPRID)

	var dispatched []ast.Page
	dispatchFn := orchestrator.FixupDispatchFunc(func(_ context.Context, page ast.Page) error {
		dispatched = append(dispatched, page)
		return nil
	})

	planned := []orchestrator.PlannedPage{
		{
			ID:          "overview.pkg-a",
			Input:       genInputFor(fixupTestRepoID),
			PackageInfo: &orchestrator.ArchitecturePackageInfo{},
		},
	}

	result, err := orchestrator.RunStubFixup(ctx, orchestrator.FixupRequest{
		RepoID:       fixupTestRepoID,
		PlannedPages: planned,
		StatusStore:  store,
		Writers:      writers,
		PageStore:    pageStore,
		PR:           pr,
		Dispatch:     dispatchFn,
	})

	if err != nil {
		t.Fatalf("RunStubFixup error: %v", err)
	}
	if result.PagesFixedUp != 1 {
		t.Errorf("PagesFixedUp = %d; want 1", result.PagesFixedUp)
	}
	if result.PagesFailed != 0 {
		t.Errorf("PagesFailed = %d; want 0", result.PagesFailed)
	}
	if len(dispatched) != 1 {
		t.Fatalf("dispatch called %d times; want 1", len(dispatched))
	}
	// Fixed-up page must have no stubs.
	if dispatched[0].HasStubMarkers() {
		t.Error("fixed-up page still has stub markers after fixup")
	}
	// UpdateFixupStatus should have been called with Done.
	if len(store.updateFixupCalls) == 0 {
		t.Fatal("UpdateFixupStatus was never called")
	}
	if store.updateFixupCalls[0].FixupStatus != livingwiki.FixupStatusDone {
		t.Errorf("UpdateFixupStatus status = %q; want %q",
			store.updateFixupCalls[0].FixupStatus, livingwiki.FixupStatusDone)
	}
}

// TestFixupPassIsIdempotent verifies L2: running the fix-up pass when no rows
// are pending is a no-op (dispatch never called, result is all-zeros).
func TestFixupPassIsIdempotent(t *testing.T) {
	ctx := context.Background()

	store := &fakeStatusStore{}
	// No pending rows — the page is already done.
	store.rows = []livingwiki.PagePublishStatusRow{
		{
			RepoID:          fixupTestRepoID,
			PageID:          "overview.pkg-a",
			SinkKind:        fixupTestSink,
			IntegrationName: fixupTestInteg,
			FixupStatus:     livingwiki.FixupStatusDone,
			Status:          "ready",
		},
	}

	pageStore := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR(fixupTestPRID)
	nsw, _ := namedFakeWriter(fixupTestInteg, markdown.SinkKindConfluence)

	dispatches := 0
	dispatchFn := orchestrator.FixupDispatchFunc(func(_ context.Context, _ ast.Page) error {
		dispatches++
		return nil
	})

	result, err := orchestrator.RunStubFixup(ctx, orchestrator.FixupRequest{
		RepoID:       fixupTestRepoID,
		PlannedPages: nil,
		StatusStore:  store,
		Writers:      []sinks.NamedSinkWriter{nsw},
		PageStore:    pageStore,
		PR:           pr,
		Dispatch:     dispatchFn,
	})

	if err != nil {
		t.Fatalf("RunStubFixup error: %v", err)
	}
	if dispatches != 0 {
		t.Errorf("dispatch called %d times on no-op pass; want 0", dispatches)
	}
	if result.PagesFixedUp != 0 || result.PagesDeferred != 0 || result.PagesFailed != 0 {
		t.Errorf("unexpected non-zero result on no-op pass: %+v", result)
	}
}

// TestFixupPassDeferredWhenTargetsStillPending verifies that a page whose stub
// targets are still pending (not ready on all sinks) is left deferred.
func TestFixupPassDeferredWhenTargetsStillPending(t *testing.T) {
	ctx := context.Background()

	store := &fakeStatusStore{}
	store.rows = []livingwiki.PagePublishStatusRow{
		// Page A is pending; its target pkg-b is NOT yet ready.
		{
			RepoID:            fixupTestRepoID,
			PageID:            "overview.pkg-a",
			SinkKind:          fixupTestSink,
			IntegrationName:   fixupTestInteg,
			FixupStatus:       livingwiki.FixupStatusPending,
			StubTargetPageIDs: []string{"overview.pkg-b"},
			Status:            "ready",
		},
		// pkg-b is still "generating" → not ready.
		{
			RepoID:          fixupTestRepoID,
			PageID:          "overview.pkg-b",
			SinkKind:        fixupTestSink,
			IntegrationName: fixupTestInteg,
			FixupStatus:     livingwiki.FixupStatusNone,
			Status:          "generating",
		},
	}

	pageStore := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR(fixupTestPRID)
	nsw, _ := namedFakeWriter(fixupTestInteg, markdown.SinkKindConfluence)

	dispatches := 0
	dispatchFn := orchestrator.FixupDispatchFunc(func(_ context.Context, _ ast.Page) error {
		dispatches++
		return nil
	})

	result, err := orchestrator.RunStubFixup(ctx, orchestrator.FixupRequest{
		RepoID: fixupTestRepoID,
		PlannedPages: []orchestrator.PlannedPage{
			{ID: "overview.pkg-a", Input: genInputFor(fixupTestRepoID), PackageInfo: &orchestrator.ArchitecturePackageInfo{}},
		},
		StatusStore: store,
		Writers:     []sinks.NamedSinkWriter{nsw},
		PageStore:   pageStore,
		PR:          pr,
		Dispatch:    dispatchFn,
	})

	if err != nil {
		t.Fatalf("RunStubFixup error: %v", err)
	}
	if dispatches != 0 {
		t.Errorf("dispatch called %d times; want 0 (target still pending)", dispatches)
	}
	if result.PagesDeferred != 1 {
		t.Errorf("PagesDeferred = %d; want 1", result.PagesDeferred)
	}
	if result.PagesFixedUp != 0 || result.PagesFailed != 0 {
		t.Errorf("unexpected fixed/failed counts: fixed=%d failed=%d", result.PagesFixedUp, result.PagesFailed)
	}
}

// TestFixupPassSkipNeedsFixupBucketFlowsIntoQueue verifies that multiple
// pages with fixup_status=pending are all processed in one pass when their
// targets are all ready.
func TestFixupPassSkipNeedsFixupBucketFlowsIntoQueue(t *testing.T) {
	ctx := context.Background()

	store := &fakeStatusStore{}
	store.rows = []livingwiki.PagePublishStatusRow{
		{RepoID: fixupTestRepoID, PageID: "overview.pkg-a", SinkKind: fixupTestSink, IntegrationName: fixupTestInteg,
			FixupStatus: livingwiki.FixupStatusPending, StubTargetPageIDs: []string{"overview.pkg-b"}, Status: "ready"},
		{RepoID: fixupTestRepoID, PageID: "overview.pkg-c", SinkKind: fixupTestSink, IntegrationName: fixupTestInteg,
			FixupStatus: livingwiki.FixupStatusPending, StubTargetPageIDs: []string{"overview.pkg-b"}, Status: "ready"},
		// Shared ready target.
		{RepoID: fixupTestRepoID, PageID: "overview.pkg-b", SinkKind: fixupTestSink, IntegrationName: fixupTestInteg,
			FixupStatus: livingwiki.FixupStatusNone, Status: "ready"},
	}

	pageStore := orchestrator.NewMemoryPageStore()
	_ = pageStore.SetProposed(ctx, fixupTestRepoID, fixupTestPRID, makeArchPageFixup("overview.pkg-a", nil))
	_ = pageStore.SetProposed(ctx, fixupTestRepoID, fixupTestPRID, makeArchPageFixup("overview.pkg-c", nil))

	nsw, _ := namedFakeWriter(fixupTestInteg, markdown.SinkKindConfluence)
	pr := orchestrator.NewMemoryWikiPR(fixupTestPRID)

	dispatches := 0
	dispatchFn := orchestrator.FixupDispatchFunc(func(_ context.Context, _ ast.Page) error {
		dispatches++
		return nil
	})

	result, err := orchestrator.RunStubFixup(ctx, orchestrator.FixupRequest{
		RepoID: fixupTestRepoID,
		PlannedPages: []orchestrator.PlannedPage{
			{ID: "overview.pkg-a", Input: genInputFor(fixupTestRepoID), PackageInfo: &orchestrator.ArchitecturePackageInfo{}},
			{ID: "overview.pkg-c", Input: genInputFor(fixupTestRepoID), PackageInfo: &orchestrator.ArchitecturePackageInfo{}},
		},
		StatusStore: store,
		Writers:     []sinks.NamedSinkWriter{nsw},
		PageStore:   pageStore,
		PR:          pr,
		Dispatch:    dispatchFn,
	})

	if err != nil {
		t.Fatalf("RunStubFixup error: %v", err)
	}
	if result.PagesFixedUp != 2 {
		t.Errorf("PagesFixedUp = %d; want 2", result.PagesFixedUp)
	}
	if dispatches != 2 {
		t.Errorf("dispatch called %d times; want 2", dispatches)
	}
}

// TestFixupPassFailedUpsertIsRecordedNotEscalated verifies that a dispatch
// error for one page sets fixup_status=failed and does NOT abort processing
// of the remaining pages.
func TestFixupPassFailedUpsertIsRecordedNotEscalated(t *testing.T) {
	ctx := context.Background()

	store := &fakeStatusStore{}
	store.rows = []livingwiki.PagePublishStatusRow{
		{RepoID: fixupTestRepoID, PageID: "overview.pkg-fail", SinkKind: fixupTestSink, IntegrationName: fixupTestInteg,
			FixupStatus: livingwiki.FixupStatusPending, StubTargetPageIDs: []string{"overview.pkg-b"}, Status: "ready"},
		{RepoID: fixupTestRepoID, PageID: "overview.pkg-ok", SinkKind: fixupTestSink, IntegrationName: fixupTestInteg,
			FixupStatus: livingwiki.FixupStatusPending, StubTargetPageIDs: []string{"overview.pkg-b"}, Status: "ready"},
		{RepoID: fixupTestRepoID, PageID: "overview.pkg-b", SinkKind: fixupTestSink, IntegrationName: fixupTestInteg,
			FixupStatus: livingwiki.FixupStatusNone, Status: "ready"},
	}

	pageStore := orchestrator.NewMemoryPageStore()
	_ = pageStore.SetProposed(ctx, fixupTestRepoID, fixupTestPRID, makeArchPageFixup("overview.pkg-fail", nil))
	_ = pageStore.SetProposed(ctx, fixupTestRepoID, fixupTestPRID, makeArchPageFixup("overview.pkg-ok", nil))

	nsw, _ := namedFakeWriter(fixupTestInteg, markdown.SinkKindConfluence)
	pr := orchestrator.NewMemoryWikiPR(fixupTestPRID)

	dispatchFn := orchestrator.FixupDispatchFunc(func(_ context.Context, page ast.Page) error {
		if page.ID == "overview.pkg-fail" {
			return fmt.Errorf("injected dispatch error")
		}
		return nil
	})

	result, err := orchestrator.RunStubFixup(ctx, orchestrator.FixupRequest{
		RepoID: fixupTestRepoID,
		PlannedPages: []orchestrator.PlannedPage{
			{ID: "overview.pkg-fail", Input: genInputFor(fixupTestRepoID), PackageInfo: &orchestrator.ArchitecturePackageInfo{}},
			{ID: "overview.pkg-ok", Input: genInputFor(fixupTestRepoID), PackageInfo: &orchestrator.ArchitecturePackageInfo{}},
		},
		StatusStore: store,
		Writers:     []sinks.NamedSinkWriter{nsw},
		PageStore:   pageStore,
		PR:          pr,
		Dispatch:    dispatchFn,
	})

	if err != nil {
		t.Fatalf("RunStubFixup returned top-level error; want nil (errors are per-page)")
	}
	if result.PagesFailed != 1 {
		t.Errorf("PagesFailed = %d; want 1", result.PagesFailed)
	}
	if result.PagesFixedUp != 1 {
		t.Errorf("PagesFixedUp = %d; want 1 (the non-failing page)", result.PagesFixedUp)
	}
	// Verify the failed page was recorded with FixupStatusFailed.
	var sawFailed bool
	for _, call := range store.updateFixupCalls {
		if call.PageID == "overview.pkg-fail" && call.FixupStatus == livingwiki.FixupStatusFailed {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("UpdateFixupStatus(FixupStatusFailed) not called for the failing page")
	}
}
