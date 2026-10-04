package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"jobsapp/internal/db"
)

// backfillTestDB opens a migrated in-memory database with one company and one job, and returns the job
// id. The row mimics what the listings scrape used to write: the JobPosting JSON is stored verbatim,
// and the columns it could have filled are NULL.
func backfillTestDB(t *testing.T, rawData string) (*sql.DB, int64) {
	t.Helper()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name) VALUES (1, 'acme', 'Acme')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var id int64
	if err := database.QueryRow(
		`INSERT INTO job_listings (company_id, listing_url, title, raw_data)
		 VALUES (1, 'https://jobs.acme.test/job/1', 'Engineer', ?) RETURNING id`,
		rawData).Scan(&id); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return database, id
}

const postingWithStructure = `{"@type":"JobPosting","title":"Engineer","datePosted":"2026-02-06T21:01:54",
  "employmentType":"FULL_TIME","jobLocationType":"TELECOMMUTE","description":"d"}`

func TestScanFindsRowsWhoseColumnsAreEmpty(t *testing.T) {
	database, id := backfillTestDB(t, postingWithStructure)

	scanned, parsed, remoteDeclared, todo, err := scan(context.Background(), database, 0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scanned != 1 || parsed != 1 {
		t.Errorf("scanned/parsed = %d/%d, want 1/1", scanned, parsed)
	}
	if remoteDeclared != 1 {
		t.Errorf("remoteDeclared = %d, want 1 (TELECOMMUTE)", remoteDeclared)
	}
	if len(todo) != 1 {
		t.Fatalf("todo = %+v, want one row", todo)
	}
	if todo[0].id != id || todo[0].employmentType != "full_time" {
		t.Errorf("todo[0] = %+v, want id %d with employment_type full_time", todo[0], id)
	}
	want := time.Date(2026, 2, 6, 21, 1, 54, 0, time.UTC)
	if todo[0].postedAt == nil || !todo[0].postedAt.Equal(want) {
		t.Errorf("postedAt = %v, want %v", todo[0].postedAt, want)
	}
}

func TestApplyWritesAndIsIdempotent(t *testing.T) {
	database, id := backfillTestDB(t, postingWithStructure)

	_, _, _, todo, err := scan(context.Background(), database, 0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, p := range todo {
		if err := apply(context.Background(), database, p); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	var (
		employment string
		postedAt   string
		lastSeen   string
	)
	if err := database.QueryRow(
		`SELECT employment_type, posted_at, last_seen_at FROM job_listings WHERE id = ?`, id).
		Scan(&employment, &postedAt, &lastSeen); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if employment != "full_time" {
		t.Errorf("employment_type = %q, want full_time", employment)
	}
	if postedAt != "2026-02-06T21:01:54Z" {
		t.Errorf("posted_at = %q, want 2026-02-06T21:01:54Z", postedAt)
	}

	// The second pass must find nothing: that is what makes the command safe to re-run.
	_, _, _, again, err := scan(context.Background(), database, 0)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second scan queued %+v, want nothing", again)
	}
}

// A backfill re-reads a capture the scraper already made; it must not claim a fresh sighting.
func TestApplyLeavesLastSeenAlone(t *testing.T) {
	database, id := backfillTestDB(t, postingWithStructure)
	if _, err := database.Exec(
		`UPDATE job_listings SET last_seen_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`, id); err != nil {
		t.Fatalf("pin last_seen_at: %v", err)
	}

	_, _, _, todo, err := scan(context.Background(), database, 0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, p := range todo {
		if err := apply(context.Background(), database, p); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	var lastSeen string
	if err := database.QueryRow(
		`SELECT last_seen_at FROM job_listings WHERE id = ?`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("read last_seen_at: %v", err)
	}
	if lastSeen != "2020-01-01T00:00:00.000Z" {
		t.Errorf("last_seen_at = %q, want it untouched", lastSeen)
	}
}

// The RemoteOK feed's raw_data is JSON too, and must not be read as a JobPosting.
func TestScanIgnoresNonJobPostingRows(t *testing.T) {
	database, _ := backfillTestDB(t, `{"id":"1137412","position":"Staff Engineer","description":"<p>hi</p>"}`)

	scanned, parsed, _, todo, err := scan(context.Background(), database, 0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scanned != 1 {
		t.Errorf("scanned = %d, want 1", scanned)
	}
	if parsed != 0 {
		t.Errorf("parsed = %d, want 0", parsed)
	}
	if len(todo) != 0 {
		t.Errorf("todo = %+v, want nothing", todo)
	}
}
