// churn_reads_test.go: tests for the run-to-run careers-URL change query.
package store

import (
	"context"
	"database/sql"
	"testing"
)

func seedChurnCompany(t *testing.T, database *sql.DB, id int64, slug, name, membership string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name, index_membership) VALUES (?, ?, ?, ?)`,
		id, slug, name, membership); err != nil {
		t.Fatalf("seed company %s: %v", slug, err)
	}
}

func seedChurnRun(t *testing.T, database *sql.DB, id int64) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO scrape_runs (id, platform_id, started_at, status)
		 VALUES (?, 23, '2026-09-26T00:00:00.000Z', 'ok')`, id); err != nil {
		t.Fatalf("seed run %d: %v", id, err)
	}
}

func seedChurnAttempt(t *testing.T, database *sql.DB, companyID, runID, index int64, kind, finalURL, state string) {
	t.Helper()
	var final any
	if finalURL != "" {
		final = finalURL
	}
	if _, err := database.Exec(`
INSERT INTO url_resolution_attempts
    (company_id, run_id, attempt_index, source, candidate_url, candidate_kind,
     http_status, final_url, title, validation_status, rejection_reason)
VALUES (?, ?, ?, 'nav_anchor', 'https://example.com/careers', ?, 200, ?, 'Careers', ?, ?)`,
		companyID, runID, index, kind, final, state, reasonFor(state)); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
}

func reasonFor(state string) any {
	if state == "accepted" {
		return nil
	}
	return "no_careers_signal"
}

// The base case: one company resolved to two different careers URLs in two runs.
func TestListResolutionChurnReportsAChangedURL(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	seedChurnCompany(t, database, 1, "acme", "Acme Corporation", "sp500")
	seedChurnRun(t, database, 4)
	seedChurnRun(t, database, 5)
	seedChurnAttempt(t, database, 1, 4, 0, "career_site", "https://acme.example.com/jobs", "accepted")
	seedChurnAttempt(t, database, 1, 5, 0, "career_site", "https://acme.example.com/careers", "accepted")

	rows, total, err := ListResolutionChurn(ctx, database, CompanyFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("ListResolutionChurn: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("total=%d rows=%d, want one change", total, len(rows))
	}
	r := rows[0]
	if r.FromRunID != 4 || r.ToRunID != 5 {
		t.Errorf("runs = %d -> %d, want 4 -> 5", r.FromRunID, r.ToRunID)
	}
	if r.FromURL != "https://acme.example.com/jobs" || r.ToURL != "https://acme.example.com/careers" {
		t.Errorf("urls = %q -> %q", r.FromURL, r.ToURL)
	}
	if r.Slug != "acme" || r.Name != "Acme Corporation" {
		t.Errorf("company = %s/%s", r.Slug, r.Name)
	}
	if !r.FromTitle.Valid || r.FromTitle.String != "Careers" {
		t.Errorf("from title = %+v", r.FromTitle)
	}
	if r.FromAt == "" || r.ToAt == "" {
		t.Errorf("timestamps missing: %q %q", r.FromAt, r.ToAt)
	}
}

// A stable URL is not churn, however many runs re-resolved it.
func TestListResolutionChurnIgnoresAStableURL(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	seedChurnCompany(t, database, 1, "acme", "Acme Corporation", "sp500")
	seedChurnRun(t, database, 4)
	seedChurnRun(t, database, 5)
	seedChurnAttempt(t, database, 1, 4, 0, "career_site", "https://acme.example.com/careers", "accepted")
	seedChurnAttempt(t, database, 1, 5, 0, "career_site", "https://acme.example.com/careers", "accepted")

	rows, total, err := ListResolutionChurn(ctx, database, CompanyFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("ListResolutionChurn: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Errorf("total=%d rows=%d, want no change for a stable URL", total, len(rows))
	}
}

// Only accepted careers-kind attempts count: a rejected candidate, a homepage, and the empty-final-url
// sitemap noise the full run wrote must all be invisible to the query.
func TestListResolutionChurnOnlyCountsAcceptedCareersAttempts(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	seedChurnCompany(t, database, 1, "acme", "Acme Corporation", "sp500")
	seedChurnRun(t, database, 4)
	seedChurnRun(t, database, 5)

	// Run 4 accepted a careers URL.
	seedChurnAttempt(t, database, 1, 4, 0, "career_site", "https://acme.example.com/careers", "accepted")
	// Run 5 contains only noise: a rejected careers candidate, an accepted homepage, and an
	// accepted career-kind row with no final URL.
	seedChurnAttempt(t, database, 1, 5, 0, "career_site", "", "rejected")
	seedChurnAttempt(t, database, 1, 5, 1, "website", "https://acme.example.com/", "accepted")
	seedChurnAttempt(t, database, 1, 5, 2, "career_site", "", "accepted")

	rows, total, err := ListResolutionChurn(ctx, database, CompanyFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("ListResolutionChurn: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Errorf("total=%d rows=%d, want noise ignored", total, len(rows))
	}
}

// A company that resolved, changed, and changed back produces two transitions, oldest first within
// a company, and the list is ordered newest transition first overall.
func TestListResolutionChurnReportsEachTransition(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	seedChurnCompany(t, database, 1, "acme", "Acme Corporation", "sp500")
	seedChurnRun(t, database, 4)
	seedChurnRun(t, database, 5)
	seedChurnRun(t, database, 6)
	seedChurnAttempt(t, database, 1, 4, 0, "career_site", "https://a.example.com/", "accepted")
	seedChurnAttempt(t, database, 1, 5, 0, "career_site", "https://b.example.com/", "accepted")
	seedChurnAttempt(t, database, 1, 6, 0, "career_site", "https://a.example.com/", "accepted")

	rows, total, err := ListResolutionChurn(ctx, database, CompanyFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("ListResolutionChurn: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("total=%d rows=%d, want two transitions", total, len(rows))
	}
	if rows[0].ToRunID != 6 || rows[1].ToRunID != 5 {
		t.Errorf("order = %d then %d, want newest transition first", rows[0].ToRunID, rows[1].ToRunID)
	}
}

// Index and search narrow the change list, and an unknown index is refused rather than ignored.
func TestListResolutionChurnFilters(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	seedChurnCompany(t, database, 1, "acme", "Acme Corporation", "sp500")
	seedChurnCompany(t, database, 2, "beta", "Beta Industries", "sp600")
	seedChurnRun(t, database, 4)
	seedChurnRun(t, database, 5)
	for _, id := range []int64{1, 2} {
		url := "https://a.example.com/"
		if id == 2 {
			url = "https://b.example.com/"
		}
		seedChurnAttempt(t, database, id, 4, 0, "career_site", url, "accepted")
		seedChurnAttempt(t, database, id, 5, 0, "career_site", url+"changed", "accepted")
	}

	rows, total, err := ListResolutionChurn(ctx, database, CompanyFilter{IndexMembership: "sp600"}, 50, 0)
	if err != nil {
		t.Fatalf("ListResolutionChurn: %v", err)
	}
	if total != 1 || rows[0].Slug != "beta" {
		t.Errorf("index filter: total=%d rows=%v", total, rows)
	}

	rows, total, err = ListResolutionChurn(ctx, database, CompanyFilter{Search: "acme"}, 50, 0)
	if err != nil {
		t.Fatalf("ListResolutionChurn: %v", err)
	}
	if total != 1 || rows[0].Slug != "acme" {
		t.Errorf("search filter: total=%d rows=%v", total, rows)
	}

	if _, _, err := ListResolutionChurn(ctx, database, CompanyFilter{IndexMembership: "sp900"}, 50, 0); err == nil {
		t.Error("an unknown index was accepted")
	}
}
