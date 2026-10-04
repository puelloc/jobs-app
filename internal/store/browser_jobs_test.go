// browser_jobs_test.go: tests for the browser-sourced job upsert against a real SQLite file.
package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// seedTwoCompaniesOnOneVendor inserts two companies that share the eightfold vendor platform (31).
// The per-company listings scrape keys its postings by that vendor, so this is the shape that makes
// company-scoped stale-marking necessary rather than merely tidy.
func seedTwoCompaniesOnOneVendor(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, c := range []struct {
		id         int64
		slug, name string
	}{{1, "alpha", "Alpha"}, {2, "beta", "Beta"}} {
		if _, err := database.Exec(
			`INSERT INTO companies (id, slug, name) VALUES (?, ?, ?)`, c.id, c.slug, c.name); err != nil {
			t.Fatalf("seed company %s: %v", c.slug, err)
		}
	}
}

// insertBrowserJob writes one posting for a company on vendor platform 31 and returns its row id.
func insertBrowserJob(t *testing.T, database *sql.DB, companyID int64, externalID string) int64 {
	t.Helper()
	job := BrowserJob{
		ExternalID: externalID,
		ListingURL: "https://jobs.example.test/job/" + externalID,
		Title:      "Engineer " + externalID,
	}
	id, _, err := UpsertBrowserJob(context.Background(), database, job, companyID, 31, nil)
	if err != nil {
		t.Fatalf("UpsertBrowserJob %s: %v", externalID, err)
	}
	return id
}

// TestMarkCompanyJobsStale_ClosesOnlyThatCompanyOnTheVendor is the scoping test: two companies share
// one vendor platform, so a company-scoped closure must leave the sibling company's postings alone.
func TestMarkCompanyJobsStale_ClosesOnlyThatCompanyOnTheVendor(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	seedTwoCompaniesOnOneVendor(t, database)

	alphaKept := insertBrowserJob(t, database, 1, "alpha-1")
	alphaDropped := insertBrowserJob(t, database, 1, "alpha-2")
	betaKept := insertBrowserJob(t, database, 2, "beta-1")

	closed, err := MarkCompanyJobsStale(ctx, database, 1, 31, map[string]struct{}{"alpha-1": {}})
	if err != nil {
		t.Fatalf("MarkCompanyJobsStale: %v", err)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1 (only alpha-2 was unseen)", closed)
	}

	for id, want := range map[int64]string{
		alphaKept:    "open",
		alphaDropped: "closed",
		betaKept:     "open", // same vendor, different company: untouched
	} {
		if got := readJobRow(t, database, id).status; got != want {
			t.Errorf("job %d status = %q, want %q", id, got, want)
		}
	}
}

// design: Freshness contract / Reappearance - a closed posting is reopened by the next run that
// observes it, keeping its original first_seen_at. Mirrors TestUpsertJobReopensAClosedRow.
func TestMarkCompanyJobsStale_ReopensOnReobservation(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	seedTwoCompaniesOnOneVendor(t, database)

	id := insertBrowserJob(t, database, 1, "alpha-1")
	first := readJobRow(t, database, id)

	closed, err := MarkCompanyJobsStale(ctx, database, 1, 31, map[string]struct{}{"never-seen": {}})
	if err != nil {
		t.Fatalf("MarkCompanyJobsStale: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	if got := readJobRow(t, database, id).status; got != "closed" {
		t.Fatalf("status = %q after stale-marking, want closed", got)
	}

	if _, _, err := UpsertBrowserJob(ctx, database, BrowserJob{
		ExternalID: "alpha-1",
		ListingURL: "https://jobs.example.test/job/alpha-1",
		Title:      "Engineer alpha-1",
	}, 1, 31, nil); err != nil {
		t.Fatalf("re-observation: %v", err)
	}

	reopened := readJobRow(t, database, id)
	if reopened.status != "open" {
		t.Errorf("status = %q after re-observation, want open", reopened.status)
	}
	if reopened.firstSeenAt != first.firstSeenAt {
		t.Errorf("first_seen_at = %q, want the original %q preserved across the cycle",
			reopened.firstSeenAt, first.firstSeenAt)
	}
}

// design: Freshness contract / "Two guards" - an empty observed set must not close a company's board.
func TestMarkCompanyJobsStale_EmptySeenSetClosesNothing(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	seedTwoCompaniesOnOneVendor(t, database)

	id := insertBrowserJob(t, database, 1, "alpha-1")

	closed, err := MarkCompanyJobsStale(ctx, database, 1, 31, map[string]struct{}{})
	if err != nil {
		t.Fatalf("MarkCompanyJobsStale with an empty seen set: %v", err)
	}
	if closed != 0 {
		t.Errorf("closed = %d, want 0", closed)
	}
	if got := readJobRow(t, database, id).status; got != "open" {
		t.Errorf("status = %q after an empty seen set, want open", got)
	}
}

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

// The listings-URL cache is what lets a re-sweep skip the agent, so the round trip it depends on -
// cold, set, clear - is worth pinning.
func TestCompanyListingsURLCacheRoundTrip(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name, career_site_url) VALUES (1, 'acme', 'Acme', 'https://acme.test/careers')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	cold, err := ScrapeCompanyBySlug(ctx, database, "acme")
	if err != nil {
		t.Fatalf("ScrapeCompanyBySlug: %v", err)
	}
	if cold.ListingsURL != "" || cold.ListingsURLResolvedAt != "" {
		t.Errorf("cold cache = %q/%q, want both empty", cold.ListingsURL, cold.ListingsURLResolvedAt)
	}
	if cold.ListingsURLRemoteConfirmed != nil {
		t.Errorf("cold verdict = %v, want nil (no agent has resolved this company)", *cold.ListingsURLRemoteConfirmed)
	}

	const url = "https://jobs.acme.test/search?query=engineer&remote=1"
	if err := SetCompanyListingsURL(ctx, database, 1, url, true); err != nil {
		t.Fatalf("SetCompanyListingsURL: %v", err)
	}

	warm, err := ScrapeCompanyBySlug(ctx, database, "acme")
	if err != nil {
		t.Fatalf("ScrapeCompanyBySlug after set: %v", err)
	}
	if warm.ListingsURL != url {
		t.Errorf("ListingsURL = %q, want %q", warm.ListingsURL, url)
	}
	if _, err := time.Parse(time.RFC3339, warm.ListingsURLResolvedAt); err != nil {
		t.Errorf("ListingsURLResolvedAt = %q, want RFC3339: %v", warm.ListingsURLResolvedAt, err)
	}
	if warm.ListingsURLRemoteConfirmed == nil || !*warm.ListingsURLRemoteConfirmed {
		t.Errorf("verdict = %v, want true", warm.ListingsURLRemoteConfirmed)
	}

	// A negative verdict is the case caching exists for: without it the company re-runs the agent on
	// every sweep.
	if err := SetCompanyListingsURL(ctx, database, 1, url, false); err != nil {
		t.Fatalf("SetCompanyListingsURL(false): %v", err)
	}
	negative, err := ScrapeCompanyBySlug(ctx, database, "acme")
	if err != nil {
		t.Fatalf("ScrapeCompanyBySlug after negative set: %v", err)
	}
	if negative.ListingsURLRemoteConfirmed == nil || *negative.ListingsURLRemoteConfirmed {
		t.Errorf("verdict = %v, want false", negative.ListingsURLRemoteConfirmed)
	}

	if err := ClearCompanyListingsURL(ctx, database, 1); err != nil {
		t.Fatalf("ClearCompanyListingsURL: %v", err)
	}
	cleared, err := ScrapeCompanyBySlug(ctx, database, "acme")
	if err != nil {
		t.Fatalf("ScrapeCompanyBySlug after clear: %v", err)
	}
	if cleared.ListingsURL != "" || cleared.ListingsURLResolvedAt != "" {
		t.Errorf("after clear = %q/%q, want both empty", cleared.ListingsURL, cleared.ListingsURLResolvedAt)
	}
	if cleared.ListingsURLRemoteConfirmed != nil {
		t.Errorf("verdict after clear = %v, want nil", *cleared.ListingsURLRemoteConfirmed)
	}
}

func TestUpsertBrowserJob_KeepsTheSameIDWhenAListingClosesAndReturns(t *testing.T) {
	// This is the property the cross-app join rests on. apply-app stores jobs_listing_id and never sees
	// this table again, so an id that changed when a posting disappeared for a week and came back would
	// leave an application pointing at a job that is no longer the one applied to - silently, because
	// the id would still resolve to *a* row. A returning posting must be found by its natural key and
	// updated in place, never re-created.
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO companies (id, slug, name) VALUES (1, 'twilio', 'Twilio')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	job := BrowserJob{
		ExternalID: "1099552857643",
		ListingURL: "https://jobs.twilio.com/careers/job/1099552857643",
		Title:      "Staff Software Engineer",
		IsRemote:   true,
	}

	id, inserted, err := UpsertBrowserJob(ctx, database, job, 1, 31, nil)
	if err != nil || !inserted {
		t.Fatalf("first upsert: id=%d inserted=%v err=%v", id, inserted, err)
	}

	// A sweep that no longer sees the posting closes it.
	if _, err := database.Exec(`UPDATE job_listings SET status = 'closed' WHERE id = ?`, id); err != nil {
		t.Fatalf("close listing: %v", err)
	}

	// The next sweep sees it again.
	id2, inserted2, err := UpsertBrowserJob(ctx, database, job, 1, 31, nil)
	if err != nil {
		t.Fatalf("re-upsert after close: %v", err)
	}
	if inserted2 {
		t.Error("a returning listing must be updated in place, not re-inserted")
	}
	if id2 != id {
		t.Errorf("returning listing id = %d, want the original %d", id2, id)
	}

	var status string
	if err := database.QueryRow(`SELECT status FROM job_listings WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "open" {
		t.Errorf("status = %q, want open: a listing that came back must reopen, or it stays invisible", status)
	}

	var count int64
	if err := database.QueryRow(`SELECT COUNT(*) FROM job_listings`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1", count)
	}
}
