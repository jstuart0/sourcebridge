// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import "testing"

func TestPagination_EncodeDecodeCursor(t *testing.T) {
	cursor := encodeCursor(50)
	if cursor == "" {
		t.Fatal("expected non-empty cursor for offset > 0")
	}
	offset, err := decodeCursor(cursor)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if offset != 50 {
		t.Errorf("expected offset 50, got %d", offset)
	}
}

func TestPagination_EmptyCursorIsZero(t *testing.T) {
	offset, err := decodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor should decode cleanly, got %v", err)
	}
	if offset != 0 {
		t.Errorf("expected offset 0, got %d", offset)
	}
}

func TestPagination_InvalidCursorErrors(t *testing.T) {
	if _, err := decodeCursor("not-valid-base64!@#"); err == nil {
		t.Error("expected error on invalid base64")
	}
}

func TestPagination_Slice(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	// Page 1, limit 3
	page, next, total := paginateSlice(items, 0, 3, 100, 500)
	if total != 10 {
		t.Errorf("expected total 10, got %d", total)
	}
	if len(page) != 3 || page[0] != 1 || page[2] != 3 {
		t.Errorf("unexpected page: %v", page)
	}
	if next == "" {
		t.Error("expected non-empty next_cursor on non-final page")
	}

	// Follow the cursor
	offset, _ := decodeCursor(next)
	page2, next2, _ := paginateSlice(items, offset, 3, 100, 500)
	if len(page2) != 3 || page2[0] != 4 {
		t.Errorf("unexpected page 2: %v", page2)
	}
	if next2 == "" {
		t.Error("expected next_cursor on page 2 (still more items)")
	}
}

func TestPagination_Slice_FinalPageNoCursor(t *testing.T) {
	items := []int{1, 2, 3}
	_, next, _ := paginateSlice(items, 0, 10, 100, 500)
	if next != "" {
		t.Errorf("expected empty next_cursor on final page, got %q", next)
	}
}

func TestPagination_Slice_OffsetBeyondEnd(t *testing.T) {
	items := []int{1, 2, 3}
	page, next, total := paginateSlice(items, 10, 5, 100, 500)
	if len(page) != 0 {
		t.Errorf("expected empty page when offset > total, got %v", page)
	}
	if next != "" {
		t.Error("no next cursor when offset > total")
	}
	if total != 3 {
		t.Errorf("total should still reflect full item count, got %d", total)
	}
}

func TestPagination_Slice_LimitClampedToCap(t *testing.T) {
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}
	page, _, _ := paginateSlice(items, 0, 1000, 100, 500)
	// caller's limit=1000 is above cap=500 → clamp to 500.
	if len(page) != 500 {
		t.Errorf("expected page clamped to cap (500), got %d", len(page))
	}
}
