// run_reads_test.go: tests for the run read query against a real SQLite file.
package store

import (
	"context"
	"database/sql"
	"testing"
)

// seedRun inserts one scrape_runs row with explicit timestamps and status so the
// ordering and nullability under test are deterministic rather than clock-based.
func seedRun(t *testing.T, db *sql.DB, id int64, startedAt string, finishedAt any, status string, errText any) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO scrape_runs (
		    id, platform_id, started_at, finished_at, status,
		    items_found, items_inserted, items_updated,
		    items_wrong, items_unverifiable, dry_run, error_text
		) VALUES (?, 1, ?, ?, ?, 10, 2, 3, 4, 5, 0, ?)`,
		id, startedAt, finishedAt, status, errText,
	)
	if err != nil {
		t.Fatalf("seed run %d: %v", id, err)
	}
}

func TestListRuns_ReturnsNewestFirstWithTotal(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	// started_at increases with id, so newest-first ordering is id DESC.
	seedRun(t, database, 1, "2026-09-27T01:00:00.000Z", "2026-09-27T01:00:05.000Z", "ok", nil)
	seedRun(t, database, 2, "2026-09-27T01:10:00.000Z", nil, "running", nil)
	seedRun(t, database, 3, "2026-09-27T01:20:00.000Z", "2026-09-27T01:20:30.000Z", "error", "boom")

	runs, total, err := ListRuns(ctx, database, 100, 0, "")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(runs) != 3 {
		t.Fatalf("len(runs) = %d, want 3", len(runs))
	}

	// Newest first: id 3, then 2, then 1.
	for i, wantID := range []int64{3, 2, 1} {
		if runs[i].ID != wantID {
			t.Errorf("runs[%d].id = %d, want %d", i, runs[i].ID, wantID)
		}
	}

	// Platform name is resolved through the join, not the raw platform_id.
	for _, r := range runs {
		if r.Platform != "remoteok" {
			t.Errorf("run %d platform = %q, want remoteok (joined platforms.name)", r.ID, r.Platform)
		}
	}

	// A running row keeps finished_at NULL; a terminal row carries error_text.
	running := runs[1]
	if running.Status != "running" || running.FinishedAt.Valid {
		t.Errorf("run 2 = status %q finished_at %q, want running with NULL finished_at",
			running.Status, running.FinishedAt.String)
	}
	failed := runs[0]
	if !failed.ErrorText.Valid || failed.ErrorText.String != "boom" {
		t.Errorf("run 3 error_text = %v, want boom", failed.ErrorText)
	}
	if runs[2].ErrorText.Valid {
		t.Errorf("run 1 error_text = %q, want NULL on success", runs[2].ErrorText.String)
	}
}

func TestListRuns_HonorsLimitAndOffset(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	seedRun(t, database, 1, "2026-09-27T01:00:00.000Z", "2026-09-27T01:00:05.000Z", "ok", nil)
	seedRun(t, database, 2, "2026-09-27T01:10:00.000Z", nil, "running", nil)
	seedRun(t, database, 3, "2026-09-27T01:20:00.000Z", "2026-09-27T01:20:30.000Z", "error", "boom")

	page, total, err := ListRuns(ctx, database, 1, 1, "")
	if err != nil {
		t.Fatalf("ListRuns(limit=1, offset=1): %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (count is independent of the page)", total)
	}
	if len(page) != 1 || page[0].ID != 2 {
		t.Errorf("page = %v, want exactly run 2 (the middle row)", page)
	}
}

func TestListRuns_EmptyIsNonNil(t *testing.T) {
	database := newTestDB(t)
	runs, total, err := ListRuns(context.Background(), database, 100, 0, "")
	if err != nil {
		t.Fatalf("ListRuns on empty db: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if runs == nil {
		t.Error("runs is nil, want a non-nil empty slice")
	}
}

func TestListRuns_SearchFiltersByPlatformAndStatus(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	// Two remoteok runs (platform 1) and one career_batch run (platform 27, migration 015).
	seedRun(t, database, 1, "2026-09-27T01:00:00.000Z", "2026-09-27T01:00:05.000Z", "ok", nil)
	seedRun(t, database, 3, "2026-09-27T01:20:00.000Z", "2026-09-27T01:20:30.000Z", "error", "boom")
	if _, err := database.Exec(
		`INSERT INTO scrape_runs (
		    id, platform_id, started_at, finished_at, status,
		    items_found, items_inserted, items_updated, items_wrong, items_unverifiable, dry_run, error_text
		) VALUES (2, 27, '2026-09-27T02:00:00.000Z', '2026-09-27T02:00:10.000Z', 'ok', 0, 0, 0, 0, 0, 0, NULL)`); err != nil {
		t.Fatalf("seed batch run: %v", err)
	}

	byPlatform, total, err := ListRuns(ctx, database, 100, 0, "batch")
	if err != nil {
		t.Fatalf("ListRuns(search=batch): %v", err)
	}
	if total != 1 || len(byPlatform) != 1 || byPlatform[0].ID != 2 || byPlatform[0].Platform != "career_batch" {
		t.Errorf("search=batch -> total %d, %+v; want exactly the career_batch run (id 2)", total, byPlatform)
	}

	byStatus, total2, err := ListRuns(ctx, database, 100, 0, "ERROR")
	if err != nil {
		t.Fatalf("ListRuns(search=ERROR): %v", err)
	}
	if total2 != 1 || len(byStatus) != 1 || byStatus[0].ID != 3 {
		t.Errorf("search=ERROR -> total %d, %+v; want exactly the error run (id 3)", total2, byStatus)
	}

	all, total3, err := ListRuns(ctx, database, 100, 0, "")
	if err != nil {
		t.Fatalf("ListRuns(empty search): %v", err)
	}
	if total3 != 3 || len(all) != 3 {
		t.Errorf("empty search -> total %d, %d rows; want all 3", total3, len(all))
	}
}

func TestGetRun_ReturnsOneRun(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	seedRun(t, database, 1, "2026-09-27T01:00:00.000Z", "2026-09-27T01:00:05.000Z", "ok", nil)
	seedRun(t, database, 2, "2026-09-27T01:10:00.000Z", nil, "running", nil)

	got, err := GetRun(ctx, database, 2)
	if err != nil {
		t.Fatalf("GetRun(2): %v", err)
	}
	if got.ID != 2 || got.Status != "running" {
		t.Errorf("GetRun(2) = id %d status %q, want 2 / running", got.ID, got.Status)
	}
	if got.FinishedAt.Valid {
		t.Errorf("GetRun(2) finished_at = %q, want NULL while running", got.FinishedAt.String)
	}
}

func TestGetRun_MissingIsNoRows(t *testing.T) {
	database := newTestDB(t)
	if _, err := GetRun(context.Background(), database, 999); err != sql.ErrNoRows {
		t.Errorf("GetRun(999) err = %v, want sql.ErrNoRows", err)
	}
}
