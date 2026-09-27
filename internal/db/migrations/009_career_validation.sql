-- 009_career_validation.sql: the platform row a browser-use validation run is recorded against.
--
-- Bands from 002's header: job boards 1-9, application systems 10-19, reference sources 20-29.
-- 23 is career_resolution. A validation pass answers a different question - "is the URL we stored
-- still a careers page?" - and writes no company URL, so recording it as a resolution run would
-- make the two indistinguishable in scrape_runs, which is the one place a run's purpose is
-- declared.
--
-- Like the other reference rows, this exists because scrape_runs.platform_id is NOT NULL and the
-- run-row-first convention means every run needs a platform before it can start.
INSERT OR IGNORE INTO platforms (id, name, platform_type, base_url_pattern) VALUES
    (24, 'career_validation', 'reference', NULL);
