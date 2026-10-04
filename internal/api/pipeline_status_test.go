// pipeline_status_test.go: tests for GET /api/pipeline/status.
package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The shared fixture holds one company with no careers URL and three jobs (two open, one closed), so
// it pins the two things worth pinning: that "scrape targets" is not just "companies", and that the
// job figures separate open from total.
func TestGetPipelineStatus(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/pipeline/status")
	requireStatus(t, rec, http.StatusOK)

	var got PipelineStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}

	if got.Companies != 1 {
		t.Errorf("companies = %d, want 1", got.Companies)
	}
	if got.CareerSiteURLs != 0 {
		t.Errorf("career_site_urls = %d, want 0 (the fixture's company has no stored URL)", got.CareerSiteURLs)
	}
	if got.ScrapeTargets != 0 {
		t.Errorf("scrape_targets = %d, want 0 (no careers URL and no vendor)", got.ScrapeTargets)
	}
	if got.Jobs != 3 {
		t.Errorf("jobs = %d, want 3", got.Jobs)
	}
	if got.OpenJobs != 2 {
		t.Errorf("open_jobs = %d, want 2 (one of the three is closed)", got.OpenJobs)
	}
	if got.CompaniesWithJobs != 1 {
		t.Errorf("companies_with_jobs = %d, want 1", got.CompaniesWithJobs)
	}
}
