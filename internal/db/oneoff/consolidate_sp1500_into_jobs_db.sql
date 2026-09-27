-- One-off consolidation: move the S&P 1500 company data into the database that already existed.
--
-- Not a migration. Migrations describe the schema every database should have; this moves rows from
-- one file into another and must never run on a fresh database.
--
-- The two files had independent id sequences, so both foreign keys are remapped here:
--   companies.id   S&P 1..1498  -> new ids assigned by SQLite, joined back by slug
--   scrape_runs.id S&P 1..23    -> +1000, which no run in either file occupies
--
-- Run with:  sqlite3 jobs.db < data/sp1500-live/consolidate.sql
-- Idempotent: every insert is guarded, so a second run changes nothing.

PRAGMA foreign_keys = ON;
ATTACH 'data/sp1500-live/jobs.db' AS sp;

-- 0. Runs first: the companies below carry a verdict_run_id that references them.
-- Runs, offset so they cannot collide with the runs already in this database.
INSERT OR IGNORE INTO scrape_runs (id, platform_id, started_at, finished_at, status, items_found,
                                   items_inserted, items_updated, error_text, dry_run,
                                   items_wrong, items_unverifiable)
SELECT id + 1000, platform_id, started_at, finished_at, status, items_found,
       items_inserted, items_updated, error_text, dry_run,
       items_wrong, items_unverifiable
  FROM sp.scrape_runs;

-- 1. Companies that exist only in the S&P file. johnson-controls exists in both; it is handled by the
--    update below rather than inserted twice, because companies.slug is unique.
INSERT INTO companies (slug, name, alias, career_site_url, industry, created_at, updated_at,
                       ticker, cik, gics_sub_industry, headquarters_location, index_membership,
                       website, website_source, career_site_url_source, career_site_url_checked_at,
                       career_site_url_http_status, career_site_url_title, article_title,
                       career_site_url_verdict, career_site_url_verdict_at,
                       career_site_url_verdict_run_id, career_site_url_verdict_url)
SELECT s.slug, s.name, s.alias, s.career_site_url, s.industry, s.created_at, s.updated_at,
       s.ticker, s.cik, s.gics_sub_industry, s.headquarters_location, s.index_membership,
       s.website, s.website_source, s.career_site_url_source, s.career_site_url_checked_at,
       s.career_site_url_http_status, s.career_site_url_title, s.article_title,
       s.career_site_url_verdict, s.career_site_url_verdict_at,
       CASE WHEN s.career_site_url_verdict_run_id IS NULL
            THEN NULL ELSE s.career_site_url_verdict_run_id + 1000 END,
       s.career_site_url_verdict_url
  FROM sp.companies s
 WHERE NOT EXISTS (SELECT 1 FROM companies c WHERE c.slug = s.slug);

-- 2. The overlapping company: the S&P row is the richer one (it carries the careers URL, the verdict
--    and the enrichment), so it wins on every column except the RemoteOK contribution.
UPDATE companies
   SET career_site_url                = (SELECT s.career_site_url                FROM sp.companies s WHERE s.slug = companies.slug),
       career_site_url_source         = (SELECT s.career_site_url_source         FROM sp.companies s WHERE s.slug = companies.slug),
       career_site_url_checked_at     = (SELECT s.career_site_url_checked_at     FROM sp.companies s WHERE s.slug = companies.slug),
       career_site_url_http_status    = (SELECT s.career_site_url_http_status    FROM sp.companies s WHERE s.slug = companies.slug),
       career_site_url_title          = (SELECT s.career_site_url_title          FROM sp.companies s WHERE s.slug = companies.slug),
       career_site_url_verdict        = (SELECT s.career_site_url_verdict        FROM sp.companies s WHERE s.slug = companies.slug),
       career_site_url_verdict_at     = (SELECT s.career_site_url_verdict_at     FROM sp.companies s WHERE s.slug = companies.slug),
       career_site_url_verdict_url    = (SELECT s.career_site_url_verdict_url    FROM sp.companies s WHERE s.slug = companies.slug),
       career_site_url_verdict_run_id = (SELECT CASE WHEN s.career_site_url_verdict_run_id IS NULL
                                                     THEN NULL ELSE s.career_site_url_verdict_run_id + 1000 END
                                           FROM sp.companies s WHERE s.slug = companies.slug),
       website                        = coalesce((SELECT s.website          FROM sp.companies s WHERE s.slug = companies.slug), website),
       website_source                 = coalesce((SELECT s.website_source   FROM sp.companies s WHERE s.slug = companies.slug), website_source),
       article_title                  = coalesce((SELECT s.article_title    FROM sp.companies s WHERE s.slug = companies.slug), article_title),
       ticker                         = coalesce((SELECT s.ticker           FROM sp.companies s WHERE s.slug = companies.slug), ticker),
       cik                            = coalesce((SELECT s.cik              FROM sp.companies s WHERE s.slug = companies.slug), cik),
       gics_sub_industry              = coalesce((SELECT s.gics_sub_industry FROM sp.companies s WHERE s.slug = companies.slug), gics_sub_industry),
       headquarters_location          = coalesce((SELECT s.headquarters_location FROM sp.companies s WHERE s.slug = companies.slug), headquarters_location),
       index_membership               = coalesce((SELECT s.index_membership FROM sp.companies s WHERE s.slug = companies.slug), index_membership)
 WHERE slug IN (SELECT slug FROM sp.companies);

-- 4. The resolution and validation attempts, with both foreign keys remapped. Evidence paths are
--    absolute and still resolve: the S&P data directory is kept.
CREATE TEMP TABLE company_map AS
SELECT s.id AS old_id, c.id AS new_id
  FROM sp.companies s JOIN companies c ON c.slug = s.slug;

INSERT INTO url_resolution_attempts (company_id, run_id, attempt_index, source, candidate_url,
                                     candidate_kind, http_status, final_url, title,
                                     validation_status, rejection_reason, evidence_path, evidence,
                                     created_at)
SELECT m.new_id, a.run_id + 1000, a.attempt_index, a.source, a.candidate_url,
       a.candidate_kind, a.http_status, a.final_url, a.title,
       a.validation_status, a.rejection_reason, a.evidence_path, a.evidence,
       a.created_at
  FROM sp.url_resolution_attempts a JOIN company_map m ON m.old_id = a.company_id
 WHERE NOT EXISTS (
       SELECT 1 FROM url_resolution_attempts x
        WHERE x.company_id = m.new_id AND x.run_id = a.run_id + 1000
          AND x.attempt_index = a.attempt_index);

-- 5. company_sources is backfilled by migration 013, which ran before these rows existed. Repeat both
--    backfills; INSERT OR IGNORE makes this a no-op for anything already recorded.
INSERT OR IGNORE INTO company_sources (company_id, platform_id, source_key, first_seen_at, last_seen_at)
SELECT id,
       CASE index_membership WHEN 'sp500' THEN 20 WHEN 'sp400' THEN 21 WHEN 'sp600' THEN 22 END,
       coalesce(ticker, ''), created_at, updated_at
  FROM companies
 WHERE index_membership IN ('sp500', 'sp400', 'sp600');

INSERT OR IGNORE INTO company_sources (company_id, platform_id, source_key, first_seen_at, last_seen_at)
SELECT company_id, discovery_platform_id, '', min(first_seen_at), max(last_seen_at)
  FROM job_listings
 WHERE discovery_platform_id IS NOT NULL
 GROUP BY company_id, discovery_platform_id;

DETACH sp;
