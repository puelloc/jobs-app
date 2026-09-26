-- 007_url_resolution.sql: the careers-resolution audit trail, plus a dry-run marker on runs.
--
-- 005 was reserved during planning and never used; the gap is permanent because applied
-- migrations are never renumbered.
--
-- Why an attempts table rather than columns on companies: the plan's premise is that a bad pick is
-- auditable after the fact. A wrong careers URL is only diagnosable if the candidates that were
-- tried, and why each was refused, are on record. One row per candidate, accepted or not.

-- scrape_runs gains a dry-run marker.
--
-- A dry run's whole value is seeing what *would* have happened, which means its attempts have to be
-- recorded - otherwise the output is counters with nothing behind them. Recording them needs a way
-- to keep them out of production queries. A flag on the run is that mechanism, and it is a better
-- fit than overloading error_text, which means "this run failed".
--
-- Every query that reports on real work must filter `dry_run = 0`. Nothing in the tree reads
-- scrape_runs for reporting yet, so there is no existing query to update; this is recorded here so
-- the first one is written correctly.
ALTER TABLE scrape_runs ADD COLUMN dry_run INTEGER NOT NULL DEFAULT 0
    CHECK (dry_run IN (0,1));

CREATE TABLE url_resolution_attempts (
    id                INTEGER PRIMARY KEY,
    company_id        INTEGER NOT NULL REFERENCES companies(id),
    -- A run row is written before any attempt, so this is only null for a manual or historical
    -- insert. The ordering is a convention the writer enforces, not something SQL can require.
    run_id            INTEGER REFERENCES scrape_runs(id),
    -- Position of this candidate within its company's resolution. It exists because the evidence
    -- path is named <company>/<run-id>/<attempt-index>-<host>, and without the index in the row
    -- there is no way to tell a genuine collision from a legitimate second attempt.
    attempt_index     INTEGER NOT NULL DEFAULT 0,
    source            TEXT NOT NULL,
    candidate_url     TEXT NOT NULL,
    candidate_kind    TEXT NOT NULL CHECK (candidate_kind IN ('website','career_site','ats_board')),
    http_status       INTEGER,
    final_url         TEXT,
    title             TEXT,
    validation_status TEXT NOT NULL CHECK (validation_status IN ('accepted','rejected','error')),
    rejection_reason  TEXT,
    evidence_path     TEXT,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    -- The reason vocabulary itself is closed in Go (internal/careers/reasons.go); SQLite cannot
    -- call an enum. What SQL can enforce is the shape: an accepted attempt has no reason, and a
    -- non-accepted one must have exactly one.
    CHECK (
        (validation_status =  'accepted' AND rejection_reason IS NULL)
     OR (validation_status != 'accepted' AND rejection_reason IS NOT NULL)
    )
);

-- A company is resolved in one pass, so the same (run, company, index) must not be written twice.
-- A violation means the writer ran a company twice, which is a bug rather than a retry, and the
-- evidence path would have collided anyway.
CREATE UNIQUE INDEX unique_url_resolution_attempts_run_slot
    ON url_resolution_attempts (run_id, company_id, attempt_index)
    WHERE run_id IS NOT NULL;

CREATE INDEX index_url_resolution_attempts_company ON url_resolution_attempts(company_id);
CREATE INDEX index_url_resolution_attempts_run     ON url_resolution_attempts(run_id);
-- The "what failed and why" query is the one run after every large pass, so the reason is indexed.
CREATE INDEX index_url_resolution_attempts_reason  ON url_resolution_attempts(rejection_reason);

-- Vendors the ATS fingerprint can identify. These are identity rows only: the fetching for them
-- lives in M2b, because Oracle Cloud, Eightfold, Workday, Phenom, SuccessFactors and Taleo have no
-- public job-board JSON API and have to be read as HTML.
--
-- No row for SmartRecruiters beyond the existing 15. Its public postings API returned byte-identical
-- 200 responses for a real company and a nonexistent one, so it cannot corroborate anything and no
-- tier was written for it.
INSERT OR IGNORE INTO platforms (id, name, platform_type, base_url_pattern) VALUES
    (30, 'oraclecloud',    'application_system', NULL),
    (31, 'eightfold',      'application_system', NULL),
    (32, 'phenom',         'application_system', NULL),
    (33, 'successfactors', 'application_system', NULL),
    (34, 'taleo',          'application_system', NULL);
