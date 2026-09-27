-- 013_company_sources.sql: record which sources contributed a company, generically.
--
-- The schema already treats *sources* as data everywhere it matters: `platforms` is a registry with a
-- type and an id band, `job_listings.discovery_platform_id` says which source found a listing, and
-- `scrape_runs.platform_id` says which source a run was for. Companies were the exception. A company
-- could only record one kind of contribution, in columns named after one source: `index_membership`
-- holds 'sp500' / 'sp400' / 'sp600', and `ticker`, `cik`, `gics_sub_industry` and
-- `headquarters_location` are that source's attributes sitting on every row.
--
-- The consequence was structural, not cosmetic: a second source of companies - another index, an
-- exchange listing, an import - had nowhere to record its contribution without a new column per
-- source, and a company present in two sources could not say so at all.
--
-- This table is that place. One row per (company, source), with the source's own key for the company
-- when it has one (a ticker, an issuer id) and the run that first and last saw it.
--
-- `index_membership` and friends are left in place: the S&P bootstrap still writes them and dropping
-- them is a separate change that has to move that write first. They are now one source's attributes
-- rather than the schema's only idea of provenance, and a reader should prefer `company_sources`.
CREATE TABLE company_sources (
    id            INTEGER PRIMARY KEY,
    company_id    INTEGER NOT NULL REFERENCES companies(id),
    platform_id   INTEGER NOT NULL REFERENCES platforms(id),
    -- The source's own identifier for this company: a ticker for an index, an employer slug for a job
    -- board. Empty when the source has no stable key, which is a fact about the source rather than a
    -- missing value, so it is '' and never NULL.
    source_key    TEXT NOT NULL DEFAULT '',
    first_seen_at TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL,
    -- A source contributes a company once. A second contribution is an update of last_seen_at, which
    -- is what makes re-running a source's import idempotent.
    UNIQUE (company_id, platform_id, source_key)
);

CREATE INDEX index_company_sources_company  ON company_sources(company_id);
CREATE INDEX index_company_sources_platform ON company_sources(platform_id);

-- Backfill: every S&P constituent's membership becomes a contribution from the index page that
-- listed it. Platform ids 20/21/22 are the three wikipedia reference rows from migration 004.
INSERT OR IGNORE INTO company_sources (company_id, platform_id, source_key, first_seen_at, last_seen_at)
SELECT id,
       CASE index_membership WHEN 'sp500' THEN 20 WHEN 'sp400' THEN 21 WHEN 'sp600' THEN 22 END,
       coalesce(ticker, ''),
       created_at,
       updated_at
  FROM companies
 WHERE index_membership IN ('sp500', 'sp400', 'sp600');

-- Backfill: a job board contributes a company as well. The scraper creates the company row from the
-- employer name on the listing, so every employer seen on a board is a contribution from that board.
-- The employer string itself is not kept on `companies`, so the key is empty; a listing-side key can
-- be added later without changing this table.
INSERT OR IGNORE INTO company_sources (company_id, platform_id, source_key, first_seen_at, last_seen_at)
SELECT company_id, discovery_platform_id, '',
       min(first_seen_at), max(last_seen_at)
  FROM job_listings
 WHERE discovery_platform_id IS NOT NULL
 GROUP BY company_id, discovery_platform_id;
