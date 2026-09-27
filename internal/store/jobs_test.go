// jobs_test.go: tests for the job_listings write paths.
//
// These run against a real migrated SQLite database (newTestDB from
// runs_test.go) because the behavior under test - the ON CONFLICT target, the
// RETURNING row count, the NOT IN predicate - only exists in SQLite.
package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"jobsapp/internal/scraper/remoteok"
)

func strPtr(s string) *string { return &s }

// testJob builds a NormalizedJob with every constant column set the way the
// RemoteOK normalizer sets it, so the store is exercised with realistic input.
func testJob(externalID string) remoteok.NormalizedJob {
	url := "https://remoteOK.com/remote-jobs/staff-engineer-acme-" + externalID
	description := "<p>We build robots.</p>"
	tags := `["dev","golang"]`
	postedAt := time.Date(2026, 9, 21, 16, 0, 11, 0, time.UTC)

	return remoteok.NormalizedJob{
		ExternalID:     externalID,
		CompanyName:    "Acme Robotics",
		Title:          "Staff Engineer",
		ListingURL:     url,
		ApplicationURL: &url,
		DiscoveryURL:   nil,
		EmploymentType: nil,
		IsRemote:       true,
		LocationText:   strPtr("Berlin"),
		Country:        nil,
		IsUS:           nil,
		Description:    &description,
		SalaryMinCents: nil,
		SalaryMaxCents: nil,
		SalaryCurrency: nil,
		SalaryPeriod:   nil,
		TagsJSON:       &tags,
		PostedAt:       &postedAt,
		RawData:        `{"id":"` + externalID + `","position":"Staff Engineer"}`,
	}
}

// jobRowState is the subset of a stored row the tests assert on.
type jobRowState struct {
	status      string
	firstSeenAt string
	lastSeenAt  string
	createdAt   string
	updatedAt   string
}

func readJobRow(t *testing.T, database *sql.DB, id int64) jobRowState {
	t.Helper()

	var got jobRowState
	if err := database.QueryRow(
		`SELECT status, first_seen_at, last_seen_at, created_at, updated_at
		   FROM job_listings WHERE id = ?`, id).
		Scan(&got.status, &got.firstSeenAt, &got.lastSeenAt, &got.createdAt, &got.updatedAt); err != nil {
		t.Fatalf("read job row %d: %v", id, err)
	}
	return got
}

func countRows(t *testing.T, database *sql.DB, query string, args ...any) int64 {
	t.Helper()

	var n int64
	if err := database.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count with %q: %v", query, err)
	}
	return n
}

func TestUpsertJobIsIdempotent(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	companyID, err := ResolveOrCreateCompany(ctx, database, "Acme Robotics")
	if err != nil {
		t.Fatalf("ResolveOrCreateCompany: %v", err)
	}

	job := testJob("1137412")

	firstID, firstInserted, err := UpsertJob(ctx, database, job, companyID, 1)
	if err != nil {
		t.Fatalf("first UpsertJob: %v", err)
	}
	if !firstInserted {
		t.Error("first upsert reported inserted = false, want true")
	}
	first := readJobRow(t, database, firstID)

	// The bookkeeping timestamps have millisecond precision, so let the clock
	// move before observing the job again.
	time.Sleep(20 * time.Millisecond)

	secondID, secondInserted, err := UpsertJob(ctx, database, job, companyID, 1)
	if err != nil {
		t.Fatalf("second UpsertJob: %v", err)
	}
	if secondInserted {
		t.Error("second upsert reported inserted = true, want false (the row already existed)")
	}
	if secondID != firstID {
		t.Errorf("second upsert id = %d, want the same row %d", secondID, firstID)
	}
	second := readJobRow(t, database, secondID)

	if got := countRows(t, database, `SELECT COUNT(*) FROM job_listings`); got != 1 {
		t.Errorf("job_listings holds %d rows after two upserts of the same job, want 1", got)
	}
	if second.firstSeenAt != first.firstSeenAt {
		t.Errorf("first_seen_at = %q after re-observation, want it preserved as %q", second.firstSeenAt, first.firstSeenAt)
	}
	if second.createdAt != first.createdAt {
		t.Errorf("created_at = %q after re-observation, want it preserved as %q", second.createdAt, first.createdAt)
	}
	if second.lastSeenAt <= first.lastSeenAt {
		t.Errorf("last_seen_at = %q after re-observation, want it to advance past %q", second.lastSeenAt, first.lastSeenAt)
	}
	if second.updatedAt <= first.updatedAt {
		t.Errorf("updated_at = %q after re-observation, want it to advance past %q", second.updatedAt, first.updatedAt)
	}

	// The upsert must land inside the partial unique index, not beside it.
	if got := countRows(t, database,
		`SELECT COUNT(*) FROM job_listings WHERE discovery_platform_id = 1 AND external_id = ?`,
		job.ExternalID); got != 1 {
		t.Errorf("rows for external_id %s = %d, want 1", job.ExternalID, got)
	}
}

// design: Freshness contract / Reappearance - a closed row is reopened by the
// next run that observes it, and keeps its original first_seen_at.
func TestUpsertJobReopensAClosedRow(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	companyID, err := ResolveOrCreateCompany(ctx, database, "Acme Robotics")
	if err != nil {
		t.Fatalf("ResolveOrCreateCompany: %v", err)
	}

	id, inserted, err := UpsertJob(ctx, database, testJob("1137412"), companyID, 1)
	if err != nil || !inserted {
		t.Fatalf("insert: id=%d inserted=%v err=%v", id, inserted, err)
	}
	first := readJobRow(t, database, id)

	// Close it the way a run that did not see it would.
	if _, err := MarkJobsStale(ctx, database, 1, map[string]struct{}{"9999999": {}}); err != nil {
		t.Fatalf("MarkJobsStale: %v", err)
	}
	if got := readJobRow(t, database, id).status; got != "closed" {
		t.Fatalf("status = %q after stale-marking, want closed", got)
	}

	_, insertedAgain, err := UpsertJob(ctx, database, testJob("1137412"), companyID, 1)
	if err != nil {
		t.Fatalf("re-observation: %v", err)
	}
	if insertedAgain {
		t.Error("re-observation reported inserted = true, want false")
	}

	reopened := readJobRow(t, database, id)
	if reopened.status != "open" {
		t.Errorf("status = %q after re-observation, want open", reopened.status)
	}
	if reopened.firstSeenAt != first.firstSeenAt {
		t.Errorf("first_seen_at = %q, want the original %q preserved across the cycle", reopened.firstSeenAt, first.firstSeenAt)
	}
}

func TestMarkJobsStaleClosesOnlyUnseenOpenRows(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	companyID, err := ResolveOrCreateCompany(ctx, database, "Acme Robotics")
	if err != nil {
		t.Fatalf("ResolveOrCreateCompany: %v", err)
	}
	for _, externalID := range []string{"1", "2", "3"} {
		if _, _, err := UpsertJob(ctx, database, testJob(externalID), companyID, 1); err != nil {
			t.Fatalf("UpsertJob %s: %v", externalID, err)
		}
	}

	closed, err := MarkJobsStale(ctx, database, 1, map[string]struct{}{"1": {}, "2": {}})
	if err != nil {
		t.Fatalf("MarkJobsStale: %v", err)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1 (only external_id 3 was unseen)", closed)
	}

	for externalID, want := range map[string]string{"1": "open", "2": "open", "3": "closed"} {
		var status string
		if err := database.QueryRow(
			`SELECT status FROM job_listings WHERE external_id = ?`, externalID).Scan(&status); err != nil {
			t.Fatalf("read status for %s: %v", externalID, err)
		}
		if status != want {
			t.Errorf("external_id %s status = %q, want %q", externalID, status, want)
		}
	}

	// Stale-marking is idempotent: the closed row is no longer 'open', so a
	// second pass over the same seen set closes nothing.
	closedAgain, err := MarkJobsStale(ctx, database, 1, map[string]struct{}{"1": {}, "2": {}})
	if err != nil {
		t.Fatalf("second MarkJobsStale: %v", err)
	}
	if closedAgain != 0 {
		t.Errorf("second pass closed = %d, want 0", closedAgain)
	}
}

// design: Freshness contract / "Two guards" - an empty seen set must not close
// the whole board.
func TestMarkJobsStaleWithEmptySeenSetClosesNothing(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	companyID, err := ResolveOrCreateCompany(ctx, database, "Acme Robotics")
	if err != nil {
		t.Fatalf("ResolveOrCreateCompany: %v", err)
	}
	for _, externalID := range []string{"1", "2"} {
		if _, _, err := UpsertJob(ctx, database, testJob(externalID), companyID, 1); err != nil {
			t.Fatalf("UpsertJob %s: %v", externalID, err)
		}
	}

	closed, err := MarkJobsStale(ctx, database, 1, map[string]struct{}{})
	if err != nil {
		t.Fatalf("MarkJobsStale with an empty seen set returned an error: %v", err)
	}
	if closed != 0 {
		t.Errorf("closed = %d, want 0", closed)
	}
	if got := countRows(t, database, `SELECT COUNT(*) FROM job_listings WHERE status = 'open'`); got != 2 {
		t.Errorf("open rows = %d, want both rows untouched", got)
	}
}

// The batch runs in one transaction (design: Run lifecycle step 10), so the
// functions must work through a *sql.Tx and not just a *sql.DB.
func TestUpsertAndStaleMarkingWorkInsideOneTransaction(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	companyID, err := ResolveOrCreateCompany(ctx, tx, "Acme Robotics")
	if err != nil {
		t.Fatalf("ResolveOrCreateCompany in tx: %v", err)
	}

	inserted := 0
	for _, externalID := range []string{"1", "2"} {
		_, wasInserted, err := UpsertJob(ctx, tx, testJob(externalID), companyID, 1)
		if err != nil {
			t.Fatalf("UpsertJob %s in tx: %v", externalID, err)
		}
		if wasInserted {
			inserted++
		}
	}
	if inserted != 2 {
		t.Errorf("inserted = %d, want 2", inserted)
	}

	closed, err := MarkJobsStale(ctx, tx, 1, map[string]struct{}{"1": {}})
	if err != nil {
		t.Fatalf("MarkJobsStale in tx: %v", err)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1", closed)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got := countRows(t, database, `SELECT COUNT(*) FROM job_listings`); got != 2 {
		t.Errorf("job_listings rows = %d, want 2 after commit", got)
	}
	if got := countRows(t, database, `SELECT COUNT(*) FROM job_listings WHERE status = 'closed'`); got != 1 {
		t.Errorf("closed rows = %d, want 1 after commit", got)
	}
	if got := countRows(t, database, `SELECT COUNT(*) FROM companies`); got != 1 {
		t.Errorf("companies rows = %d, want 1", got)
	}
}

// design: docs/remoteok-mapping.md, "Company name handling" - "Bjak " has a
// trailing space on 3 rows, and the slug is what keeps them one company.
func TestResolveOrCreateCompanyNormalizesSlug(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	first, err := ResolveOrCreateCompany(ctx, database, "Bjak ")
	if err != nil {
		t.Fatalf("ResolveOrCreateCompany(\"Bjak \"): %v", err)
	}
	second, err := ResolveOrCreateCompany(ctx, database, "Bjak")
	if err != nil {
		t.Fatalf("ResolveOrCreateCompany(\"Bjak\"): %v", err)
	}
	if first != second {
		t.Errorf("ids = %d and %d, want the same company for a trailing-space variant", first, second)
	}

	third, err := ResolveOrCreateCompany(ctx, database, "American Bureau of Shipping (ABS)")
	if err != nil {
		t.Fatalf("ResolveOrCreateCompany(ABS): %v", err)
	}
	var slug string
	if err := database.QueryRow(`SELECT slug FROM companies WHERE id = ?`, third).Scan(&slug); err != nil {
		t.Fatalf("read slug: %v", err)
	}
	if slug != "american-bureau-of-shipping-abs" {
		t.Errorf("slug = %q, want american-bureau-of-shipping-abs", slug)
	}

	if _, err := ResolveOrCreateCompany(ctx, database, "   "); err == nil {
		t.Error("ResolveOrCreateCompany accepted a name with no usable characters, want an error")
	}
}
