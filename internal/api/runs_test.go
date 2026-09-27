// runs_test.go: black-box tests for GET /api/runs.
//
// Same discipline as handlers_test.go: drive the real router over a real
// migrated SQLite database and assert on the HTTP response, decoding into local
// types that mirror the contract rather than the api package's own types.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- contract mirror --------------------------------------------------------

type runJSON struct {
	ID                int64   `json:"id"`
	Platform          string  `json:"platform"`
	Status            string  `json:"status"`
	StartedAt         string  `json:"started_at"`
	FinishedAt        *string `json:"finished_at"`
	ItemsFound        int64   `json:"items_found"`
	ItemsInserted     int64   `json:"items_inserted"`
	ItemsUpdated      int64   `json:"items_updated"`
	ItemsWrong        int64   `json:"items_wrong"`
	ItemsUnverifiable int64   `json:"items_unverifiable"`
	DryRun            bool    `json:"dry_run"`
	ErrorText         *string `json:"error_text"`
}

type runsListJSON struct {
	Runs   []runJSON `json:"runs"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
	Total  int64     `json:"total"`
}

func decodeRuns(t *testing.T, rec *httptest.ResponseRecorder) runsListJSON {
	t.Helper()
	var got runsListJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode runs response %q: %v", rec.Body.String(), err)
	}
	return got
}

// seedRunsDB inserts runs directly into a fresh test database and returns the
// real router wired to it.
func seedRunsDB(t *testing.T) http.Handler {
	t.Helper()
	handler, database := newTestServerAndDB(t)

	const insert = `
INSERT INTO scrape_runs (
    id, platform_id, started_at, finished_at, status,
    items_found, items_inserted, items_updated,
    items_wrong, items_unverifiable, dry_run, error_text
) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	for _, r := range []struct {
		id          int64
		startedAt   string
		finishedAt  any
		status      string
		found       int64
		inserted    int64
		updated     int64
		wrong       int64
		unverifiable int64
		dryRun      int64
		errText     any
	}{
		{1, "2026-09-27T01:00:00.000Z", "2026-09-27T01:00:05.000Z", "ok", 100, 99, 1, 0, 0, 0, nil},
		{2, "2026-09-27T01:10:00.000Z", nil, "running", 0, 0, 0, 0, 0, 0, nil},
		{3, "2026-09-27T01:20:00.000Z", "2026-09-27T01:20:30.000Z", "error", 50, 0, 0, 0, 0, 1, "boom"},
	} {
		if _, err := database.Exec(insert,
			r.id, r.startedAt, r.finishedAt, r.status,
			r.found, r.inserted, r.updated, r.wrong, r.unverifiable, r.dryRun, r.errText,
		); err != nil {
			t.Fatalf("seed run %d: %v", r.id, err)
		}
	}
	return handler
}

// --- list endpoint ----------------------------------------------------------

func TestListRuns_ReturnsEnvelope(t *testing.T) {
	rec := do(t, seedRunsDB(t), http.MethodGet, "/api/runs")

	requireStatus(t, rec, http.StatusOK)
	requireJSON(t, rec)

	got := decodeRuns(t, rec)
	if got.Total != 3 {
		t.Errorf("total = %d, want 3", got.Total)
	}
	if got.Limit != 25 {
		t.Errorf("limit = %d, want the documented default 25", got.Limit)
	}
	if got.Offset != 0 {
		t.Errorf("offset = %d, want 0", got.Offset)
	}
	if len(got.Runs) != 3 {
		t.Fatalf("len(runs) = %d, want 3 (body: %s)", len(got.Runs), rec.Body.String())
	}

	// Newest first by started_at: id 3, 2, 1.
	for i, wantID := range []int64{3, 2, 1} {
		if got.Runs[i].ID != wantID {
			t.Errorf("runs[%d].id = %d, want %d", i, got.Runs[i].ID, wantID)
		}
	}
}

func TestListRuns_FieldMapping(t *testing.T) {
	rec := do(t, seedRunsDB(t), http.MethodGet, "/api/runs")
	requireStatus(t, rec, http.StatusOK)

	byID := map[int64]runJSON{}
	for _, r := range decodeRuns(t, rec).Runs {
		byID[r.ID] = r
	}

	ok := byID[1]
	if ok.Platform != "remoteok" {
		t.Errorf("run 1 platform = %q, want remoteok (joined platforms.name)", ok.Platform)
	}
	if ok.Status != "ok" || ok.StartedAt != "2026-09-27T01:00:00Z" {
		t.Errorf("run 1 = status %q started_at %q, want ok / RFC3339 normalized", ok.Status, ok.StartedAt)
	}
	if ok.FinishedAt == nil || *ok.FinishedAt != "2026-09-27T01:00:05Z" {
		t.Errorf("run 1 finished_at = %v, want RFC3339 normalized", ok.FinishedAt)
	}
	if ok.ItemsFound != 100 || ok.ItemsInserted != 99 || ok.ItemsUpdated != 1 {
		t.Errorf("run 1 counters = %d/%d/%d, want 100/99/1", ok.ItemsFound, ok.ItemsInserted, ok.ItemsUpdated)
	}
	if ok.ErrorText != nil {
		t.Errorf("run 1 error_text = %q, want null on success", *ok.ErrorText)
	}

	running := byID[2]
	if running.FinishedAt != nil {
		t.Errorf("run 2 finished_at = %q, want null while running", *running.FinishedAt)
	}

	failed := byID[3]
	if failed.ErrorText == nil || *failed.ErrorText != "boom" {
		t.Errorf("run 3 error_text = %v, want boom", failed.ErrorText)
	}
	if !failed.DryRun {
		t.Errorf("run 3 dry_run = false, want true (seeded dry_run = 1)")
	}
	if ok.DryRun {
		t.Errorf("run 1 dry_run = true, want false (seeded dry_run = 0)")
	}
}

func TestListRuns_ExactFieldSet(t *testing.T) {
	rec := do(t, seedRunsDB(t), http.MethodGet, "/api/runs")
	requireStatus(t, rec, http.StatusOK)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	requireKeys(t, "envelope", envelope, []string{"runs", "limit", "offset", "total"})

	var runs []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["runs"], &runs); err != nil {
		t.Fatalf("decode runs array: %v", err)
	}
	requireKeys(t, "run item", runs[0], []string{
		"id", "platform", "status", "started_at", "finished_at",
		"items_found", "items_inserted", "items_updated",
		"items_wrong", "items_unverifiable", "dry_run", "error_text",
	})
}

func TestListRuns_RejectsLimitZero(t *testing.T) {
	rec := do(t, seedRunsDB(t), http.MethodGet, "/api/runs?limit=0")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestListRuns_RejectsLimitOver100(t *testing.T) {
	rec := do(t, seedRunsDB(t), http.MethodGet, "/api/runs?limit=101")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestListRuns_RejectsNonIntegerLimit(t *testing.T) {
	rec := do(t, seedRunsDB(t), http.MethodGet, "/api/runs?limit=abc")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestListRuns_RejectsNegativeOffset(t *testing.T) {
	rec := do(t, seedRunsDB(t), http.MethodGet, "/api/runs?offset=-1")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestListRuns_HonorsLimitAndOffset(t *testing.T) {
	rec := do(t, seedRunsDB(t), http.MethodGet, "/api/runs?limit=1&offset=1")
	requireStatus(t, rec, http.StatusOK)

	got := decodeRuns(t, rec)
	if got.Total != 3 {
		t.Errorf("total = %d, want 3 (count is independent of the page)", got.Total)
	}
	if len(got.Runs) != 1 || got.Runs[0].ID != 2 {
		t.Errorf("runs = %v, want exactly run 2 (the middle row)", got.Runs)
	}
}
