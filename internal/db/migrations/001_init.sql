-- 001_init.sql: initial schema for scraped software-engineering job listings (SQLite).

-- All ids are plain INTEGER PRIMARY KEY (rowid alias). AUTOINCREMENT is deliberately NOT used:
-- we never need monotonic ids that survive rowid reuse, and omitting it avoids the extra
-- sqlite_sequence bookkeeping and unbounded id growth.

CREATE TABLE platforms (
    id               INTEGER PRIMARY KEY,
    name             TEXT UNIQUE NOT NULL,      -- stable slug, e.g. 'remoteok', 'greenhouse'
    platform_type    TEXT NOT NULL CHECK (platform_type IN ('application_system','job_board')),
    base_url_pattern TEXT
);

CREATE TABLE companies (
    id              INTEGER PRIMARY KEY,
    slug            TEXT NOT NULL UNIQUE,       -- normalized name, used for dedupe/lookup
    name            TEXT NOT NULL,
    alias           TEXT,                       -- alternate/normalized display name, if any
    career_site_url TEXT,
    industry        TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    -- updated_at is maintained by application code; intentionally no trigger.
);

-- Which application system a company's real applications live on.
CREATE TABLE company_application_platforms (
    id          INTEGER PRIMARY KEY,
    company_id  INTEGER NOT NULL REFERENCES companies(id),
    platform_id INTEGER NOT NULL REFERENCES platforms(id),
    base_url    TEXT,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (company_id, platform_id)
    -- INVARIANT: platform_id must reference a row whose platform_type = 'application_system'.
    -- SQLite has no cross-table CHECK / partial FK, so this cannot be declared here;
    -- the application layer must enforce it on insert.
);

CREATE TABLE job_listings (
    id                              INTEGER PRIMARY KEY,
    company_id                      INTEGER NOT NULL REFERENCES companies(id),
    company_application_platform_id INTEGER REFERENCES company_application_platforms(id),
    external_id                     TEXT,
    application_url                 TEXT,       -- canonical submit URL; nullable when unknown
    listing_url                     TEXT NOT NULL,  -- URL we actually scraped
    discovery_platform_id           INTEGER REFERENCES platforms(id),
    discovery_url                   TEXT,
    title                           TEXT NOT NULL,
    employment_type                 TEXT CHECK (employment_type IS NULL OR employment_type IN
                                     ('full_time','part_time','contract','intern','temporary','unknown')),
    is_remote                       INTEGER NOT NULL DEFAULT 1 CHECK (is_remote IN (0,1)),
    location_text                   TEXT,       -- free-text location as published
    country                         TEXT,       -- ISO-3166 alpha-2 when known
    is_us                           INTEGER CHECK (is_us IS NULL OR is_us IN (0,1)),
    description                     TEXT,
    salary_min_cents                INTEGER,    -- money stored as INTEGER minor units (cents)
    salary_max_cents                INTEGER,    -- money stored as INTEGER minor units (cents)
    salary_currency                 TEXT,       -- ISO-4217, e.g. 'USD'; NULL when unknown
    salary_period                   TEXT CHECK (salary_period IS NULL OR salary_period IN
                                     ('year','month','hour')),
    tags_json                       TEXT CHECK (tags_json IS NULL OR json_valid(tags_json)),
    posted_at                       TEXT,
    first_seen_at                   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_seen_at                    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    status                          TEXT NOT NULL DEFAULT 'open'
                                     CHECK (status IN ('open','closed','filled','unknown')),
    raw_data                        TEXT CHECK (raw_data IS NULL OR json_valid(raw_data)),
    created_at                      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at                      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    -- updated_at is maintained by application code; intentionally no trigger.
);

-- Dedupe rows sourced from an application system (req id inside that system).
CREATE UNIQUE INDEX unique_job_listings_by_application_system
    ON job_listings (company_application_platform_id, external_id)
    WHERE company_application_platform_id IS NOT NULL AND external_id IS NOT NULL;

-- Dedupe rows sourced from a job board (e.g. RemoteOK), where there is no application system.
CREATE UNIQUE INDEX unique_job_listings_by_discovery
    ON job_listings (discovery_platform_id, external_id)
    WHERE discovery_platform_id IS NOT NULL AND external_id IS NOT NULL;

CREATE INDEX index_job_listings_company ON job_listings(company_id);
CREATE INDEX index_job_listings_status ON job_listings(status);
CREATE INDEX index_job_listings_posted_at ON job_listings(posted_at);
CREATE INDEX index_job_listings_us_remote_posted ON job_listings(is_us, is_remote, posted_at DESC);
CREATE INDEX index_company_application_platforms_company ON company_application_platforms(company_id);

CREATE TABLE scrape_runs (
    id             INTEGER PRIMARY KEY,
    platform_id    INTEGER NOT NULL REFERENCES platforms(id),
    started_at     TEXT NOT NULL,
    finished_at    TEXT,
    status         TEXT NOT NULL DEFAULT 'running'
                     CHECK (status IN ('running','ok','error')),
    items_found    INTEGER NOT NULL DEFAULT 0,
    items_inserted INTEGER NOT NULL DEFAULT 0,
    items_updated  INTEGER NOT NULL DEFAULT 0,
    error_text     TEXT
);
