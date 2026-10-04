// pipeline_status.go: the counts the Runs page needs to say which pipeline step to run next.
//
// The page used to offer five undifferentiated buttons, so an operator had to know the order and which
// steps were already done. These counts let it state that instead: how far the
// bootstrap -> resolve -> classify chain has got, how many companies a sweep would actually visit, and
// how warm the listings-URL cache is - which is the number that decides whether a sweep will be slow
// or fast.
package store

import (
	"context"
	"fmt"
)

// PipelineStatus is the pipeline's readiness in one row.
type PipelineStatus struct {
	// Companies is every company the bootstrap has indexed.
	Companies int64
	// CareerSiteURLs is how many have a resolved careers URL (the output of `sp1500 resolve`).
	CareerSiteURLs int64
	// ScrapeTargets is how many have both a careers URL and a classified vendor: the exact set a sweep
	// visits, so it is the figure the Runs page shows next to the sweep.
	ScrapeTargets int64
	// CachedListingsURLs is how many companies carry a cached filtered listings URL, and
	// ConfirmedRemoteRoles how many of those the agent confirmed had remote roles at it. Together they
	// describe the cache: zero cached means the next sweep runs the agent for every company, while
	// cached == confirmed means it runs for none.
	CachedListingsURLs   int64
	ConfirmedRemoteRoles int64
	// Jobs / OpenJobs / CompaniesWithJobs describe what the scrapes have produced so far.
	Jobs              int64
	OpenJobs          int64
	CompaniesWithJobs int64
}

// GetPipelineStatus reads the counts in one round trip. Every subquery is a count over a small table
// with the relevant index, and the page polls it alongside the runs list.
func GetPipelineStatus(ctx context.Context, q Querier) (PipelineStatus, error) {
	const query = `
SELECT
    (SELECT COUNT(*) FROM companies),
    (SELECT COUNT(*) FROM companies
      WHERE career_site_url IS NOT NULL AND career_site_url <> ''),
    (SELECT COUNT(*) FROM companies c
       JOIN company_application_platforms cap ON cap.company_id = c.id
      WHERE c.career_site_url IS NOT NULL AND c.career_site_url <> ''),
    (SELECT COUNT(*) FROM companies WHERE listings_url IS NOT NULL),
    (SELECT COUNT(*) FROM companies WHERE listings_url_remote_confirmed = 1),
    (SELECT COUNT(*) FROM job_listings),
    (SELECT COUNT(*) FROM job_listings WHERE status = 'open'),
    (SELECT COUNT(DISTINCT company_id) FROM job_listings)`

	var s PipelineStatus
	err := q.QueryRowContext(ctx, query).Scan(
		&s.Companies,
		&s.CareerSiteURLs,
		&s.ScrapeTargets,
		&s.CachedListingsURLs,
		&s.ConfirmedRemoteRoles,
		&s.Jobs,
		&s.OpenJobs,
		&s.CompaniesWithJobs,
	)
	if err != nil {
		return PipelineStatus{}, fmt.Errorf("pipeline status: %w", err)
	}
	return s, nil
}
