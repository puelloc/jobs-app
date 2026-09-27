// browser_jobs.go: the write path for job postings extracted by the browser-use listings flow.
//
// It shares the two SQL statements with jobs.go (UpsertJob): the same (discovery_platform_id,
// external_id) partial unique index makes a browser-sourced posting idempotent across re-runs. The
// only difference is the smaller input shape - the browser flow captures title, URL, description and
// the board's embedded JSON, and every derived field (skills, years of experience) belongs to a
// later analysis pass, not to this write.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// BrowserJob is one posting extracted by worker/listings_fetch.py, ready to persist.
type BrowserJob struct {
	ExternalID     string
	ListingURL     string
	Title          string
	Description    string
	LocationText   string
	EmploymentType string
	IsRemote       bool
	PostedAt       *time.Time
	RawData        string
}

// UpsertBrowserJob writes one browser-sourced posting and reports whether it was newly inserted.
// Keyed by (discovery_platform_id, external_id) exactly like UpsertJob, so a re-run refreshes the
// row instead of duplicating it. Empty optional fields are stored as NULL, matching the schema's
// "null means not known" convention.
func UpsertBrowserJob(ctx context.Context, q Querier, j BrowserJob, companyID, discoveryPlatformID int64) (int64, bool, error) {
	var existingID int64
	lookupErr := q.QueryRowContext(ctx, jobExistsSQL, discoveryPlatformID, j.ExternalID).Scan(&existingID)
	inserted := false
	switch {
	case errors.Is(lookupErr, sql.ErrNoRows):
		inserted = true
	case lookupErr != nil:
		return 0, false, fmt.Errorf("upsert browser job %s: look up existing row: %w", j.ExternalID, lookupErr)
	}

	isRemote := int64(0)
	if j.IsRemote {
		isRemote = 1
	}
	var postedAt any
	if j.PostedAt != nil {
		postedAt = j.PostedAt.UTC().Format(time.RFC3339)
	}

	var id int64
	err := q.QueryRowContext(ctx, upsertJobSQL,
		companyID,
		j.ExternalID,
		nil, // application_url
		j.ListingURL,
		discoveryPlatformID,
		nil, // discovery_url
		j.Title,
		nullable(j.EmploymentType),
		isRemote,
		nullable(j.LocationText),
		nil, // country
		nil, // is_us
		nullable(j.Description),
		nil, nil, nil, nil, // salary_min/max/currency/period
		nil, // tags_json
		postedAt,
		nullable(j.RawData),
	).Scan(&id)
	if err != nil {
		return 0, false, fmt.Errorf("upsert browser job %s: %w", j.ExternalID, err)
	}
	return id, inserted, nil
}

// CompanyIDBySlug returns the companies id for a slug, or sql.ErrNoRows when there is no such row.
func CompanyIDBySlug(ctx context.Context, q Querier, slug string) (int64, error) {
	var id int64
	if err := q.QueryRowContext(ctx, selectCompanyBySlugSQL, slug).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// PlatformIDByName returns the platforms id for a vendor name, or sql.ErrNoRows when unknown.
func PlatformIDByName(ctx context.Context, q Querier, name string) (int64, error) {
	var id int64
	if err := q.QueryRowContext(ctx, `SELECT id FROM platforms WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// ExternalID extracts a posting's stable id from its URL using the vendor's external_id_pattern
// (one capture group). An empty or malformed pattern, or a URL the pattern does not match, falls
// back to the URL itself, which is still a unique, stable key for the (discovery_platform_id,
// external_id) partial index.
func ExternalID(pattern, url string) string {
	if pattern == "" {
		return url
	}
	re, err := regexp.Compile(pattern)
	if err != nil || re.NumSubexp() < 1 {
		return url
	}
	if m := re.FindStringSubmatch(url); m != nil {
		return m[1]
	}
	return url
}
