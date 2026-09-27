// jobs.go: the two write paths for job_listings - the idempotent upsert that
// implements run lifecycle step 8, and the stale-marking that implements step 9.
//
// design: docs/scraper-design.md, "Idempotency contract" and "Freshness
// contract"; docs/remoteok-mapping.md, "Idempotency implications".
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"jobsapp/internal/scraper/remoteok"
)

// jobExistsSQL is the pre-upsert existence check. It matches the partial unique
// index unique_job_listings_by_discovery exactly.
const jobExistsSQL = `
SELECT id FROM job_listings
 WHERE discovery_platform_id = ?
   AND external_id = ?`

// upsertJobSQL inserts one row, or updates the row the partial unique index
// unique_job_listings_by_discovery already holds for (discovery_platform_id,
// external_id). SQLite identifies a partial index in an ON CONFLICT target by
// the WHERE clause rather than by name, which is why the predicate is repeated.
//
// Columns deliberately absent from the INSERT: id (SQLite assigns it),
// company_application_platform_id (NULL for a board source),
// first_seen_at/created_at (column defaults on insert, never touched on
// conflict), and last_seen_at/updated_at (column defaults on insert, refreshed
// explicitly on conflict).
//
// On conflict the row is reopened and every content column is replaced with what
// this run parsed, which is what makes re-observation authoritative.
const upsertJobSQL = `
INSERT INTO job_listings (
    company_id,
    external_id,
    application_url,
    listing_url,
    discovery_platform_id,
    discovery_url,
    title,
    employment_type,
    is_remote,
    location_text,
    country,
    is_us,
    description,
    salary_min_cents,
    salary_max_cents,
    salary_currency,
    salary_period,
    tags_json,
    posted_at,
    status,
    raw_data
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?
)
ON CONFLICT(discovery_platform_id, external_id)
    WHERE discovery_platform_id IS NOT NULL AND external_id IS NOT NULL
DO UPDATE SET
    company_id       = excluded.company_id,
    application_url  = excluded.application_url,
    listing_url      = excluded.listing_url,
    discovery_url    = excluded.discovery_url,
    title            = excluded.title,
    employment_type  = excluded.employment_type,
    is_remote        = excluded.is_remote,
    location_text    = excluded.location_text,
    country          = excluded.country,
    is_us            = excluded.is_us,
    description      = excluded.description,
    salary_min_cents = excluded.salary_min_cents,
    salary_max_cents = excluded.salary_max_cents,
    salary_currency  = excluded.salary_currency,
    salary_period    = excluded.salary_period,
    tags_json        = excluded.tags_json,
    posted_at        = excluded.posted_at,
    last_seen_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    updated_at       = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    status           = 'open'
RETURNING id`

// UpsertJob writes one normalized job and reports whether the row was newly
// inserted (true) or an existing row was refreshed (false).
//
// Insert-vs-update is decided by a SELECT before the upsert rather than by a
// comparison in RETURNING. The caller passes one *sql.Tx for the whole batch
// (main.go), so the read and the write are serialized by that transaction and
// there is no window for the answer to go stale; a RETURNING comparison would
// also have to encode the before-image in SQL for no gain.
//
// The returned id is the job_listings rowid, whether inserted or updated.
func UpsertJob(ctx context.Context, q Querier, j remoteok.NormalizedJob, companyID int64, discoveryPlatformID int64) (id int64, inserted bool, err error) {
	var existingID int64
	lookupErr := q.QueryRowContext(ctx, jobExistsSQL, discoveryPlatformID, j.ExternalID).Scan(&existingID)
	switch {
	case errors.Is(lookupErr, sql.ErrNoRows):
		inserted = true
	case lookupErr != nil:
		return 0, false, fmt.Errorf("upsert job %s: look up existing row: %w", j.ExternalID, lookupErr)
	}

	isRemote := int64(0)
	if j.IsRemote {
		isRemote = 1
	}

	var postedAt any
	if j.PostedAt != nil {
		// The schema stores TEXT; RFC3339 UTC is what the mapping doc records for
		// posted_at and what the read API parses.
		postedAt = j.PostedAt.UTC().Format(time.RFC3339)
	}

	err = q.QueryRowContext(ctx, upsertJobSQL,
		companyID,
		j.ExternalID,
		j.ApplicationURL,
		j.ListingURL,
		discoveryPlatformID,
		j.DiscoveryURL,
		j.Title,
		j.EmploymentType,
		isRemote,
		j.LocationText,
		j.Country,
		j.IsUS,
		j.Description,
		j.SalaryMinCents,
		j.SalaryMaxCents,
		j.SalaryCurrency,
		j.SalaryPeriod,
		j.TagsJSON,
		postedAt,
		j.RawData,
	).Scan(&id)
	if err != nil {
		return 0, false, fmt.Errorf("upsert job %s: %w", j.ExternalID, err)
	}

	return id, inserted, nil
}

// MarkJobsStale closes this source's rows that the current run did not see.
//
// design: Freshness contract - closure is set membership in the seen set, never
// a clock; only rows from this discovery platform are touched, only rows that are
// currently 'open' are touched, and a row whose external_id is NULL is outside
// the comparison and so is left exactly as it was.
//
// An empty seen set is the documented guard: a run that observed nothing must
// never close the whole board, so it reports zero closures without running the
// UPDATE.
func MarkJobsStale(ctx context.Context, q Querier, discoveryPlatformID int64, seenIDs map[string]struct{}) (int64, error) {
	if len(seenIDs) == 0 {
		// design: Freshness contract / "Two guards" (the other guard is the
		// caller skipping this call when zero rows were accepted).
		return 0, nil
	}

	// SQLite bounds bound parameters per statement: SQLITE_MAX_VARIABLE_NUMBER is
	// 32766 from 3.32 on (999 before), and one parameter is the platform id. The
	// source serves 100 elements, so 99 ids sits three orders of magnitude below
	// the limit and a single inline NOT IN is safe. If the seen set ever
	// approaches the limit, split it into batches - each batch closing its own
	// subset, which composes because the predicate is per-row - or load the ids
	// into a temp table and join against it.
	placeholders := make([]string, 0, len(seenIDs))
	args := make([]any, 0, len(seenIDs)+1)
	args = append(args, discoveryPlatformID)
	for externalID := range seenIDs {
		placeholders = append(placeholders, "?")
		args = append(args, externalID)
	}

	// Querier (job_reads.go) exposes no Exec, so the UPDATE is issued through
	// QueryContext with RETURNING id. Consuming every returned row both runs the
	// UPDATE to completion and yields the affected-row count that RowsAffected
	// would otherwise provide.
	query := `
UPDATE job_listings
   SET status = 'closed',
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE discovery_platform_id = ?
   AND status = 'open'
   AND external_id NOT IN (` + strings.Join(placeholders, ", ") + `)
RETURNING id`

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("mark jobs stale for platform %d: %w", discoveryPlatformID, err)
	}
	defer rows.Close()

	var closed int64
	for rows.Next() {
		closed++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("mark jobs stale for platform %d: %w", discoveryPlatformID, err)
	}
	return closed, nil
}
