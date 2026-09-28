-- 016_scrape_runs_company.sql: link a scrape_runs row to the company it scraped.
--
-- The per-company listings scrape (cmd/scrape, launched from the UI) owns its own scrape_runs row,
-- but until now nothing recorded WHICH company that row was for. The full-sweep batch runner needs
-- "did company X already succeed / already leave a browser-use trace" so a re-run can skip companies
-- that already worked, and neither fact is recoverable today: every listings scrape shares the
-- 'career_listings' platform, and job_listings cannot distinguish "scraped and found zero" from
-- "scraped and the agent silently failed" (a success that writes zero rows is legitimate when the
-- agent found no remote roles).
--
-- company_id stays NULL for every other run kind (bootstrap, resolve, validate, classify, batch,
-- scraper): those runs are not about one company. The FK is deliberately permissive on delete to
-- match the rest of the schema; a company is never deleted in practice.
ALTER TABLE scrape_runs ADD COLUMN company_id INTEGER REFERENCES companies(id);

-- The one query this column exists for: "the latest listings run for company X" during a sweep's
-- skip decision, and "what happened" in reporting. Ordered newest-first so the batch runner can take
-- the first row.
CREATE INDEX index_scrape_runs_company
    ON scrape_runs (company_id, started_at DESC)
    WHERE company_id IS NOT NULL;
