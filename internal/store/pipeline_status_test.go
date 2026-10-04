// pipeline_status_test.go: tests for the counts behind GET /api/pipeline/status.
package store

import (
	"context"
	"testing"
)

func TestGetPipelineStatusCountsEachStage(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	// target: a careers URL and a classified vendor, so a sweep would visit it.
	exec(`INSERT INTO companies (id, slug, name, career_site_url) VALUES (1, 'target', 'Target', 'https://target.test/careers')`)
	exec(`INSERT INTO company_application_platforms (company_id, platform_id, base_url) VALUES (1, 31, 'https://jobs.target.test')`)
	// resolved: a careers URL but no vendor, so classify still has work to do.
	exec(`INSERT INTO companies (id, slug, name, career_site_url) VALUES (2, 'resolved', 'Resolved', 'https://resolved.test/careers')`)
	// classified: a vendor but no careers URL, so resolve still has work to do.
	exec(`INSERT INTO companies (id, slug, name) VALUES (3, 'classified', 'Classified')`)
	exec(`INSERT INTO company_application_platforms (company_id, platform_id, base_url) VALUES (3, 32, 'https://jobs.classified.test')`)

	// The cache: target has been resolved, and the agent confirmed remote roles there.
	exec(`UPDATE companies SET listings_url = 'https://jobs.target.test/search?q=engineer',
	      listings_url_remote_confirmed = 1 WHERE id = 1`)

	// One open posting and one closed one, both for the same company.
	exec(`INSERT INTO job_listings (company_id, listing_url, title, status) VALUES (1, 'https://jobs.target.test/job/1', 'Open Role', 'open')`)
	exec(`INSERT INTO job_listings (company_id, listing_url, title, status) VALUES (1, 'https://jobs.target.test/job/2', 'Closed Role', 'closed')`)

	got, err := GetPipelineStatus(ctx, database)
	if err != nil {
		t.Fatalf("GetPipelineStatus: %v", err)
	}

	want := PipelineStatus{
		Companies:            3,
		CareerSiteURLs:       2,
		ScrapeTargets:        1, // only the company with both a URL and a vendor
		CachedListingsURLs:   1,
		ConfirmedRemoteRoles: 1,
		Jobs:                 2,
		OpenJobs:             1,
		CompaniesWithJobs:    1,
	}
	if got != want {
		t.Errorf("status = %+v\n         want %+v", got, want)
	}
}

func TestGetPipelineStatusOnAnEmptyDatabase(t *testing.T) {
	database := newTestDB(t)

	got, err := GetPipelineStatus(context.Background(), database)
	if err != nil {
		t.Fatalf("GetPipelineStatus: %v", err)
	}
	if got != (PipelineStatus{}) {
		t.Errorf("status = %+v, want all zero", got)
	}
}
