// pipeline_status.go: GET /api/pipeline/status - the counts the Runs page uses to tell an operator
// which pipeline step to run next, instead of making them hold the pipeline's order in their head.
package api

import (
	"database/sql"
	"net/http"

	"jobsapp/internal/store"
)

// PipelineStatusResponse is the GET /api/pipeline/status response: all counts of what exists right now.
// The UI decides what counts as "done" from them, so the contract stays a set of facts rather than a
// server-side opinion about progress.
type PipelineStatusResponse struct {
	Companies            int64 `json:"companies"`
	CareerSiteURLs       int64 `json:"career_site_urls"`
	ScrapeTargets        int64 `json:"scrape_targets"`
	CachedListingsURLs   int64 `json:"cached_listings_urls"`
	ConfirmedRemoteRoles int64 `json:"confirmed_remote_roles"`
	Jobs                 int64 `json:"jobs"`
	OpenJobs             int64 `json:"open_jobs"`
	CompaniesWithJobs    int64 `json:"companies_with_jobs"`
}

// handleGetPipelineStatus serves GET /api/pipeline/status.
func handleGetPipelineStatus(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := store.GetPipelineStatus(r.Context(), db)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, PipelineStatusResponse{
			Companies:            status.Companies,
			CareerSiteURLs:       status.CareerSiteURLs,
			ScrapeTargets:        status.ScrapeTargets,
			CachedListingsURLs:   status.CachedListingsURLs,
			ConfirmedRemoteRoles: status.ConfirmedRemoteRoles,
			Jobs:                 status.Jobs,
			OpenJobs:             status.OpenJobs,
			CompaniesWithJobs:    status.CompaniesWithJobs,
		})
	}
}
