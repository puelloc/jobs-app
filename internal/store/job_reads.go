// job_reads.go: the read-only queries behind the HTTP API.
//
// Nothing in this file writes; the viewer's whole contract with the database is
// SELECT. Column semantics (status, first_seen_at, last_seen_at) come from
// docs/scraper-design.md.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Querier is the subset of *sql.DB these read functions need. Taking an
// interface rather than the concrete pool keeps the functions usable from
// inside a caller-supplied transaction (*sql.Tx satisfies it too) and makes the
// dependency explicit. *sql.DB is the normal argument.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// JobRow mirrors one job_listings row plus the joined company name. Nullable
// columns use sql.Null* so the API layer - not this package - decides how to
// present "not known". Timestamps stay sql.NullString: SQLite stores them as
// TEXT, and parsing is the API layer's job.
//
// job_listings.company_id is NOT NULL and foreign_keys is ON, so CompanyName
// should always resolve; the LEFT JOIN below is defensive, and an invalid
// CompanyName becomes "Unknown company" in the API.
type JobRow struct {
	ID             int64
	Title          string
	CompanyName    sql.NullString
	Status         string
	EmploymentType sql.NullString
	LocationText   sql.NullString
	Country        sql.NullString
	IsRemote       int64
	SalaryMinCents sql.NullInt64
	SalaryMaxCents sql.NullInt64
	SalaryCurrency sql.NullString
	SalaryPeriod   sql.NullString
	PostedAt       sql.NullString
	FirstSeenAt    string
	LastSeenAt     string
	Description    sql.NullString
	ListingURL     string
	ApplicationURL sql.NullString
	DiscoveryURL   sql.NullString
}

// jobSelect is the one projection both read paths use. It deliberately carries
// every column the API may need, including description, so the list and detail
// queries cannot disagree about a column's name or source. The list endpoint
// simply does not emit description; the cost of reading it is a non-issue for a
// single-user viewer, and the alternative is two projections that drift.
//
// Columns the API must never expose (raw_data, external_id, company_id,
// company_application_platform_id, discovery_platform_id, tags_json,
// created_at, updated_at) are not selected at all, so they cannot leak.
const jobSelect = `
SELECT
    j.id,
    j.title,
    c.name AS company_name,
    j.status,
    j.employment_type,
    j.location_text,
    j.country,
    j.is_remote,
    j.salary_min_cents,
    j.salary_max_cents,
    j.salary_currency,
    j.salary_period,
    j.posted_at,
    j.first_seen_at,
    j.last_seen_at,
    j.description,
    j.listing_url,
    j.application_url,
    j.discovery_url
FROM job_listings j
LEFT JOIN companies c ON c.id = j.company_id`

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so the two read paths
// share one column-order-sensitive Scan.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(s rowScanner) (JobRow, error) {
	var r JobRow
	err := s.Scan(
		&r.ID,
		&r.Title,
		&r.CompanyName,
		&r.Status,
		&r.EmploymentType,
		&r.LocationText,
		&r.Country,
		&r.IsRemote,
		&r.SalaryMinCents,
		&r.SalaryMaxCents,
		&r.SalaryCurrency,
		&r.SalaryPeriod,
		&r.PostedAt,
		&r.FirstSeenAt,
		&r.LastSeenAt,
		&r.Description,
		&r.ListingURL,
		&r.ApplicationURL,
		&r.DiscoveryURL,
	)
	if err != nil {
		return JobRow{}, err
	}
	return r, nil
}

// ListJobs returns one page of jobs ordered by COALESCE(posted_at,
// first_seen_at) DESC, id DESC, plus the total number of rows.
//
// The count and the page are two separate reads rather than one transaction: a
// single-user, read-only viewer has no writer racing it, and a torn count would
// at worst make "load more" flicker. Wrapping both in a transaction would pin
// the pool's single connection for longer for no user-visible gain.
//
// limit and offset are expected to be validated by the caller; they are bound
// as parameters, never interpolated.
func ListJobs(ctx context.Context, q Querier, limit, offset int) ([]JobRow, int64, error) {
	var total int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_listings`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list jobs: count: %w", err)
	}

	rows, err := q.QueryContext(ctx, jobSelect+`
ORDER BY COALESCE(j.posted_at, j.first_seen_at) DESC, j.id DESC
LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: select: %w", err)
	}
	defer rows.Close()

	// Non-nil even when empty so the JSON envelope carries [] rather than null.
	jobs := make([]JobRow, 0, limit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("list jobs: scan: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list jobs: iterate: %w", err)
	}
	return jobs, total, nil
}

// GetJobByID returns one job, or found=false when no row has that id. A missing
// row is not an error: the caller turns it into a 404.
func GetJobByID(ctx context.Context, q Querier, id int64) (JobRow, bool, error) {
	row := q.QueryRowContext(ctx, jobSelect+`
WHERE j.id = ?`, id)

	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return JobRow{}, false, nil
	}
	if err != nil {
		return JobRow{}, false, fmt.Errorf("get job by id: %w", err)
	}
	return job, true, nil
}
