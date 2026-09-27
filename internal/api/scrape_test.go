// scrape_test.go: tests for POST /api/companies/{id}/scrape.
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"jobsapp/internal/db"
)

// newScrapeTestServer returns a router whose scrape command is /usr/bin/true, so a triggered scrape
// launches harmlessly and the run stays "running" (true never finishes it).
func newScrapeTestServer(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewRouter(database, t.TempDir(), []string{"true"}), database
}

func seedClassifiedCompany(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name, career_site_url) VALUES (1, 'twilio', 'Twilio', 'https://www.twilio.com/en-us/careers')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO company_application_platforms (company_id, platform_id, base_url) VALUES (1, 31, 'https://jobs.twilio.com')`); err != nil {
		t.Fatalf("seed classification: %v", err)
	}
}

func TestScrapeCompany_ReturnsRunID(t *testing.T) {
	h, database := newScrapeTestServer(t)
	seedClassifiedCompany(t, database)

	rec := do(t, h, http.MethodPost, "/api/companies/1/scrape")
	requireStatus(t, rec, http.StatusAccepted)

	var got ScrapeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID == 0 {
		t.Fatal("run_id should be non-zero")
	}

	var status string
	if err := database.QueryRow(`SELECT status FROM scrape_runs WHERE id = ?`, got.RunID).Scan(&status); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != "running" {
		t.Errorf("run status = %q, want running", status)
	}
}

func TestScrapeCompany_RequiresClassification(t *testing.T) {
	h, database := newScrapeTestServer(t)
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name, career_site_url) VALUES (2, 'acme', 'Acme', 'https://acme.com/careers')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/api/companies/2/scrape")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestScrapeCompany_MissingCompany(t *testing.T) {
	h, _ := newScrapeTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/companies/999/scrape")
	requireError(t, rec, http.StatusNotFound, "not_found")
}

func TestScrapeCompany_UnconfiguredCommand(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	h := NewRouter(database, t.TempDir(), nil)
	rec := do(t, h, http.MethodPost, "/api/companies/1/scrape")
	requireError(t, rec, http.StatusServiceUnavailable, "internal")
}

func TestScrapeCompany_RejectsConcurrentScrape(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	// "sh -c 'sleep 5'" keeps the first scrape in flight long enough for the second request to
	// observe the gate. The extra --slug/--vendor/--run-id args become $0/$1... and are ignored.
	h := NewRouter(database, t.TempDir(), []string{"sh", "-c", "sleep 5"})
	seedClassifiedCompany(t, database)

	rec := do(t, h, http.MethodPost, "/api/companies/1/scrape")
	requireStatus(t, rec, http.StatusAccepted)

	rec2 := do(t, h, http.MethodPost, "/api/companies/1/scrape")
	requireError(t, rec2, http.StatusConflict, "conflict")
}
