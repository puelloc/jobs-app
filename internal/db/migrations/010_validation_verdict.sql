-- 010_validation_verdict.sql: record the browser-use validation pass's verdict on a stored URL.
--
-- Until now a company carried when its careers URL was last checked (career_site_url_checked_at),
-- what the server said (career_site_url_http_status) and the page title - but not what the check
-- concluded. "Which companies' stored careers URLs are confirmed, which are provably not careers
-- pages, and which could not be established" was answerable only by joining url_resolution_attempts
-- to its newest browser_use row per company, which is exactly the query an operator runs first and
-- should not have to reconstruct.
--
-- A column rather than a table. Migration 007 already holds one attempt row per candidate per run -
-- the audit trail - and the plan's section 5.3 explicitly rejected a second multi-row inventory of
-- company URLs. What was missing is current state, and current state belongs on the row that has a
-- current state.
--
-- NULL means the company has never been through a browser validation pass. That is a different fact
-- from any of the three verdicts and must stay distinguishable, so there is no default.
ALTER TABLE companies ADD COLUMN career_site_url_verdict TEXT
    CHECK (career_site_url_verdict IS NULL
           OR career_site_url_verdict IN ('confirmed','wrong','unverifiable'));

-- When the verdict above was reached. Separate from career_site_url_checked_at, which moves on every
-- resolution run as well; a resolution run produces a URL rather than judging one, and would
-- otherwise make the verdict look fresher than it is.
ALTER TABLE companies ADD COLUMN career_site_url_verdict_at TEXT;

-- The run that produced the verdict, so a surprising verdict can be traced to its attempts and its
-- on-disk evidence without guessing by timestamp.
ALTER TABLE companies ADD COLUMN career_site_url_verdict_run_id INTEGER REFERENCES scrape_runs(id);

-- The run's own split. items_found/items_inserted/items_updated cannot express it: a validation run
-- changes no company URL, so every counter but "found" would be a guess. These two are meaningful 0
-- for every other kind of run, which is why they live here rather than in a separate metrics table.
ALTER TABLE scrape_runs ADD COLUMN items_wrong INTEGER NOT NULL DEFAULT 0;
ALTER TABLE scrape_runs ADD COLUMN items_unverifiable INTEGER NOT NULL DEFAULT 0;

CREATE INDEX index_companies_career_site_url_verdict
    ON companies (career_site_url_verdict)
    WHERE career_site_url_verdict IS NOT NULL;
