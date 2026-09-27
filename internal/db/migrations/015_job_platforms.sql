-- 015_job_platforms.sql: platform rows for the one-off jobs the server can trigger from the UI.
--
-- A server-triggered job records its scrape_runs row against a stable platform name, so the runs
-- dashboard can tell a classify job from a batch sweep from a bootstrap apart. 'reference' is the
-- closest existing type; these rows identify runs, not data sources.

INSERT OR IGNORE INTO platforms (id, name, platform_type) VALUES
    (26, 'career_classify',  'reference'),
    (27, 'career_batch',     'reference'),
    (28, 'career_bootstrap', 'reference');
