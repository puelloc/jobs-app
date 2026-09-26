-- 006_company_enrichment.sql: columns the S&P 1500 bootstrap and careers resolution need.
--
-- 005 is reserved for M2's url_resolution_attempts table and the ATS vendor seeds, so the
-- numbering skips a value here. Migration ordering is lexical and each applied version is recorded
-- individually, so a gap is harmless.
--
-- companies.industry already exists and holds the GICS Sector as published.

-- Identity, as published on the three index pages. cik is stored exactly as extracted: the S&P 500
-- and S&P 600 pages carry a numeric CIK column, but the S&P 400 page has no CIK column at all and
-- only a CIK= parameter in its SEC link, which is ticker-like for 281 of its 399 rows. Resolving
-- those to numeric CIKs via SEC company_tickers.json is a later step, so this column must not be
-- assumed numeric.
ALTER TABLE companies ADD COLUMN ticker TEXT;
ALTER TABLE companies ADD COLUMN cik TEXT;
ALTER TABLE companies ADD COLUMN gics_sub_industry TEXT;
ALTER TABLE companies ADD COLUMN headquarters_location TEXT;

-- 'sp500' | 'sp400' | 'sp600'. The three lists are disjoint, so one column suffices; a company
-- that moves between indices has this value overwritten by the next run.
ALTER TABLE companies ADD COLUMN index_membership TEXT;

-- Homepage resolved from an external source (Wikidata P856, a Wikipedia infobox, or a 10-K), and
-- which source produced it. This is a stepping stone to career_site_url, not the payload.
ALTER TABLE companies ADD COLUMN website TEXT;
ALTER TABLE companies ADD COLUMN website_source TEXT;

-- Provenance and last observed state for career_site_url, which already exists. Recording how the
-- URL was found and what the fetch returned is what makes a wrong pick diagnosable later.
ALTER TABLE companies ADD COLUMN career_site_url_source TEXT;
ALTER TABLE companies ADD COLUMN career_site_url_checked_at TEXT;
ALTER TABLE companies ADD COLUMN career_site_url_http_status INTEGER;
ALTER TABLE companies ADD COLUMN career_site_url_title TEXT;
