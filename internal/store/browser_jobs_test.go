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

	id, inserted, err := UpsertBrowserJob(ctx, database, job, 1, 31, nil) // 31 = eightfold
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
	id2, inserted2, err := UpsertBrowserJob(ctx, database, job, 1, 31, nil)
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

	if _, _, err := UpsertBrowserJob(ctx, database, job, 2, 32, nil); err != nil { // 32 = phenom
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

func TestUpsertBrowserJob_RecordsScrapeRun(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO companies (id, slug, name) VALUES (1, 'twilio', 'Twilio')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO scrape_runs (id, platform_id, started_at, status, company_id) VALUES (7, 25, '2026-01-01T00:00:00.000Z', 'ok', 1)`); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	job := BrowserJob{ExternalID: "run-42", ListingURL: "https://jobs.twilio.com/job/42", Title: "Engineer"}
	if _, _, err := UpsertBrowserJob(ctx, database, job, 1, 31, int64(7)); err != nil {
		t.Fatalf("upsert with run id: %v", err)
	}

	var runID int64
	if err := database.QueryRow(`SELECT scrape_run_id FROM job_listings WHERE external_id = 'run-42'`).Scan(&runID); err != nil {
		t.Fatalf("read scrape_run_id: %v", err)
	}
	if runID != 7 {
		t.Errorf("scrape_run_id = %d, want 7", runID)
	}
}

func TestExternalID(t *testing.T) {
	cases := []struct {
		pattern, url, want string
	}{
		{`careers/job/(\d+)`, "https://jobs.twilio.com/careers/job/1099552857643?x=1", "1099552857643"},
		{`careers/job/(\d+)`, "https://jobs.twilio.com/careers/job/not-a-number", "https://jobs.twilio.com/careers/job/not-a-number"},
		{"", "https://x.example/job/42", "https://x.example/job/42"},
		{"no-capture-group", "https://x.example/job/42", "https://x.example/job/42"},
	}
	for _, c := range cases {
		if got := ExternalID(c.pattern, c.url); got != c.want {
			t.Errorf("ExternalID(%q, %q) = %q, want %q", c.pattern, c.url, got, c.want)
		}
	}
}

func TestCompanyAndPlatformLookup(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO companies (id, slug, name) VALUES (7, 'twilio', 'Twilio')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	id, err := CompanyIDBySlug(ctx, database, "twilio")
	if err != nil || id != 7 {
		t.Errorf("CompanyIDBySlug(twilio) = %d, %v; want 7", id, err)
	}
	pid, err := PlatformIDByName(ctx, database, "eightfold")
	if err != nil || pid != 31 {
		t.Errorf("PlatformIDByName(eightfold) = %d, %v; want 31", pid, err)
	}
	if _, err := CompanyIDBySlug(ctx, database, "does-not-exist"); err == nil {
		t.Error("CompanyIDBySlug(does-not-exist) should return sql.ErrNoRows")
	}
}
