-- 017_job_scrape_run.sql: record which scrape run produced each job_listings row.
--
-- A job posting extracted by the browser-use listings flow is the downstream artifact of one run:
-- cmd/scrape runs the agent (which writes the trace) and then stores the jobs that trace's decision
-- led to. Until now the job row carried no link back to that run, so a viewer could not answer "why
-- was this posting deemed a remote software-engineering role?" from the posting itself - the agent's
-- reasoning lived only in the run's trace, and nothing joined the two.
--
-- scrape_run_id is NULL for rows written before this migration and for the RemoteOK job-board source
-- (which has no agent trace to associate). The FK is permissive on delete to match the rest of the
-- schema; a run is never deleted in practice.
ALTER TABLE job_listings ADD COLUMN scrape_run_id INTEGER REFERENCES scrape_runs(id);

CREATE INDEX index_job_listings_scrape_run
    ON job_listings (scrape_run_id)
    WHERE scrape_run_id IS NOT NULL;
