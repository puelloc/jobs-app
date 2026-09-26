-- 004_reference_platforms.sql: let platforms carry reference sources, and seed the Wikipedia ones.
--
-- ID bands (kept from 002): job boards 1-9, application systems 10-19, reference sources 20-29,
-- additional ATS vendors 30+.
--
-- Why this is a rebuild rather than an INSERT: platforms.platform_type is a CHECK constraint, and
-- SQLite cannot widen one in place. The 'reference' value is not cosmetic - scrape_runs.platform_id
-- is NOT NULL and every run records itself before touching the network, so a Wikipedia run needs a
-- platforms row to point at. Pointing it at a job_board or application_system row would be a lie.

-- migrate:fk_off
--
-- company_application_platforms, job_listings and scrape_runs all reference platforms(id), so
-- DROP TABLE platforms fails with "FOREIGN KEY constraint failed" while enforcement is on. The
-- runner turns enforcement off for this migration and runs PRAGMA foreign_key_check afterwards.

CREATE TABLE platforms_new (
    id               INTEGER PRIMARY KEY,
    name             TEXT UNIQUE NOT NULL,      -- stable slug, e.g. 'remoteok', 'greenhouse'
    platform_type    TEXT NOT NULL CHECK (platform_type IN ('application_system','job_board','reference')),
    base_url_pattern TEXT
);

INSERT INTO platforms_new (id, name, platform_type, base_url_pattern)
SELECT id, name, platform_type, base_url_pattern FROM platforms;

DROP TABLE platforms;

-- Under the default legacy_alter_table=OFF this rewrites the child FK clauses from
-- "REFERENCES platforms_new(id)" back to "REFERENCES platforms(id)".
ALTER TABLE platforms_new RENAME TO platforms;

INSERT OR IGNORE INTO platforms (id, name, platform_type, base_url_pattern) VALUES
    (20, 'wikipedia_sp500', 'reference', 'https://en.wikipedia.org/wiki/List_of_S%26P_500_companies'),
    (21, 'wikipedia_sp400', 'reference', 'https://en.wikipedia.org/wiki/List_of_S%26P_400_companies'),
    (22, 'wikipedia_sp600', 'reference', 'https://en.wikipedia.org/wiki/List_of_S%26P_600_companies'),
    (23, 'career_resolution', 'reference', NULL);
