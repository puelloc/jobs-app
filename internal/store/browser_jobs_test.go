// browser_jobs_test.go: tests for the browser-sourced job upsert against a real SQLite file.
package store

import (
	"context"
	"testing"
	"time"
)

func TestUpsertBrowserJob_InsertsThenRefreshes(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO companies (id, slug, name) VALUES (1, 'twilio', 'Twilio')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	posted := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	job := BrowserJob{
		ExternalID:   "1099552857643",
		ListingURL:   "https://jobs.twilio.com/careers/job/1099552857643",
		Title:        "Staff Software Engineer",
		Description:  "Build the data platform.",
		LocationText: "Remote - US",
		IsRemote:     true,
		PostedAt:     &posted,
		RawData:      `{"@type":"JobPosting","title":"Staff Software Engineer"}`,
	}

	id, inserted, err := UpsertBrowserJob(ctx, database, job, 1, 31) // 31 = eightfold
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !inserted {
		t.Error("first upsert should insert")
	}
	if id == 0 {
		t.Error("id should be non-zero")
	}

	// A re-run with the same (platform, external_id) refreshes, never duplicates.
	job.Title = "Staff Software Engineer (L4)"
	id2, inserted2, err := UpsertBrowserJob(ctx, database, job, 1, 31)
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if inserted2 {
		t.Error("re-upsert should update, not insert")
	}
	if id2 != id {
		t.Errorf("re-upsert id = %d, want the same row %d", id2, id)
	}

	var count, isRemote int64
	var title, rawData string
	if err := database.QueryRow(`
		SELECT count(*), max(title), max(is_remote), max(raw_data)
		  FROM job_listings WHERE discovery_platform_id = 31`).Scan(&count, &title, &isRemote, &rawData); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (no duplicate)", count)
	}
	if title != "Staff Software Engineer (L4)" {
		t.Errorf("title = %q, want the refreshed value", title)
	}
	if isRemote != 1 {
		t.Errorf("is_remote = %d, want 1", isRemote)
	}
	if rawData == "" {
		t.Error("raw_data should be stored")
	}
}

func TestUpsertBrowserJob_EmptyOptionalsAreNull(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO companies (id, slug, name) VALUES (2, 'acme', 'Acme')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	job := BrowserJob{
		ExternalID: "42",
		ListingURL: "https://jobs.acme.com/job/42",
		Title:      "Engineer",
	}

	if _, _, err := UpsertBrowserJob(ctx, database, job, 2, 32); err != nil { // 32 = phenom
		t.Fatalf("upsert with empty optionals: %v", err)
	}

	var desc, location, raw any
	if err := database.QueryRow(`
		SELECT description, location_text, raw_data FROM job_listings WHERE external_id = '42'`).
		Scan(&desc, &location, &raw); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if desc != nil || location != nil || raw != nil {
		t.Errorf("optional columns should be NULL, got desc=%v location=%v raw=%v", desc, location, raw)
	}
}
