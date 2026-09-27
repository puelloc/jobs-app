// scrape_targets_test.go: tests for ListScrapeTargets against a real SQLite file.
package store

import (
	"context"
	"database/sql"
	"testing"
)

// seedScrapeTargets inserts companies in each eligibility state and returns nothing; each test
// asserts the exact membership it expects.
func seedScrapeTargets(t *testing.T, database *sql.DB) {
	t.Helper()
	// alpha: eligible (career URL + classified eightfold, platform 31).
	exec := func(q string, args ...any) {
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO companies (id, slug, name, career_site_url) VALUES (1, 'alpha', 'Alpha', 'https://alpha.com/careers')`)
	exec(`INSERT INTO company_application_platforms (company_id, platform_id, base_url) VALUES (1, 31, 'https://jobs.alpha.com')`)
	// beta: no career URL -> ineligible.
	exec(`INSERT INTO companies (id, slug, name) VALUES (2, 'beta', 'Beta')`)
	exec(`INSERT INTO company_application_platforms (company_id, platform_id, base_url) VALUES (2, 31, 'https://jobs.beta.com')`)
	// gamma: career URL but not classified -> ineligible.
	exec(`INSERT INTO companies (id, slug, name, career_site_url) VALUES (3, 'gamma', 'Gamma', 'https://gamma.com/careers')`)
	// delta: eligible, classified phenom (platform 32).
	exec(`INSERT INTO companies (id, slug, name, career_site_url) VALUES (4, 'delta', 'Delta', 'https://delta.com/careers')`)
	exec(`INSERT INTO company_application_platforms (company_id, platform_id, base_url) VALUES (4, 32, 'https://careers.delta.com')`)
}

func TestListScrapeTargets_OnlyEligibleCompanies(t *testing.T) {
	database := newTestDB(t)
	seedScrapeTargets(t, database)

	targets, err := ListScrapeTargets(context.Background(), database, 0)
	if err != nil {
		t.Fatalf("ListScrapeTargets: %v", err)
	}

	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2: %+v", len(targets), targets)
	}
	if targets[0].Slug != "alpha" || targets[0].Vendor != "eightfold" {
		t.Errorf("first target = %s/%s, want alpha/eightfold", targets[0].Slug, targets[0].Vendor)
	}
	if targets[1].Slug != "delta" || targets[1].Vendor != "phenom" {
		t.Errorf("second target = %s/%s, want delta/phenom", targets[1].Slug, targets[1].Vendor)
	}
}

func TestListScrapeTargets_Limit(t *testing.T) {
	database := newTestDB(t)
	seedScrapeTargets(t, database)

	targets, err := ListScrapeTargets(context.Background(), database, 1)
	if err != nil {
		t.Fatalf("ListScrapeTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Slug != "alpha" {
		t.Errorf("limited target = %q, want alpha", targets[0].Slug)
	}
}

func TestListScrapeTargets_Empty(t *testing.T) {
	database := newTestDB(t)
	targets, err := ListScrapeTargets(context.Background(), database, 0)
	if err != nil {
		t.Fatalf("ListScrapeTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("got %d targets, want 0", len(targets))
	}
}
