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
	// Country is the ISO-3166 alpha-2 code parsed from the posting's ld+json; empty means unknown.
	Country string
	// IsUS reports whether the posting's location is the United States. Nil means the location was
	// not present in the ld+json, so the posting cannot be confirmed US.
	IsUS     *bool
	PostedAt *time.Time
	RawData  string
}

// UpsertBrowserJob writes one browser-sourced posting and reports whether it was newly inserted.
// Keyed by (discovery_platform_id, external_id) exactly like UpsertJob, so a re-run refreshes the
// row instead of duplicating it. Empty optional fields are stored as NULL, matching the schema's
// "null means not known" convention.
// scrapeRunID is any so the two callers can pass a real run id (cmd/scrape) or nil for NULL
// (cmd/listings, a standalone debug command with no run to associate).
func UpsertBrowserJob(ctx context.Context, q Querier, j BrowserJob, companyID, discoveryPlatformID int64, scrapeRunID any) (int64, bool, error) {
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
	var isUS any
	if j.IsUS != nil {
		if *j.IsUS {
			isUS = int64(1)
		} else {
			isUS = int64(0)
		}
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
		nullable(j.Country),
		isUS,
		nullable(j.Description),
		nil, nil, nil, nil, // salary_min/max/currency/period
		nil, // tags_json
		postedAt,
		nullable(j.RawData),
		scrapeRunID,
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

// ScrapeCompany is the subset of a companies row a listings scrape needs.
type ScrapeCompany struct {
	ID            int64
	Slug          string
	Name          string
	CareerSiteURL string
	// ListingsURL is the filtered listings URL a previous scrape resolved and cached, or "" when the
	// company has never been resolved. ListingsURLResolvedAt is when, as SQLite's RFC3339 UTC text
	// ("" alongside an empty URL), which is what the TTL decision reads.
	ListingsURL           string
	ListingsURLResolvedAt string
	// ListingsURLRemoteConfirmed is the verdict the resolving agent reached: true when it found remote
	// software-engineering roles at ListingsURL, false when it found none. Nil means no agent has
	// resolved this company. A re-sweep reads it to skip the agent for a company that had nothing while
	// still knowing how to read that URL.
	ListingsURLRemoteConfirmed *bool
}

// ScrapeCompanyBySlug returns the scrape inputs for a slug, or sql.ErrNoRows when there is no such
// row. A NULL career_site_url comes back as "" so the caller can report "no stored URL" rather than
// mistaking it for a missing company.
func ScrapeCompanyBySlug(ctx context.Context, q Querier, slug string) (ScrapeCompany, error) {
	var (
		c         ScrapeCompany
		confirmed sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `
SELECT id, slug, name,
       COALESCE(career_site_url, ''),
       COALESCE(listings_url, ''),
       COALESCE(listings_url_resolved_at, ''),
       listings_url_remote_confirmed
  FROM companies WHERE slug = ?`, slug).
		Scan(&c.ID, &c.Slug, &c.Name, &c.CareerSiteURL, &c.ListingsURL, &c.ListingsURLResolvedAt, &confirmed)
	if err != nil {
		return c, err
	}
	if confirmed.Valid {
		v := confirmed.Int64 != 0
		c.ListingsURLRemoteConfirmed = &v
	}
	return c, nil
}

// SetCompanyListingsURL caches the filtered listings URL a scrape resolved for a company, together with
// the verdict the agent reached at it, so the next sweep can skip the agent that found both. The
// timestamp is database-generated, matching every other timestamp in the schema.
func SetCompanyListingsURL(ctx context.Context, q Querier, companyID int64, url string, remoteConfirmed bool) error {
	confirmed := 0
	if remoteConfirmed {
		confirmed = 1
	}

	var id int64
	err := q.QueryRowContext(ctx, `
UPDATE companies
   SET listings_url                  = ?,
       listings_url_resolved_at      = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       listings_url_remote_confirmed = ?,
       updated_at                    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id = ?
RETURNING id`, url, confirmed, companyID).Scan(&id)
	if err != nil {
		return fmt.Errorf("cache listings url for company %d: %w", companyID, err)
	}
	return nil
}

// ClearCompanyListingsURL drops the cached URL so the next scrape re-resolves it with the agent.
func ClearCompanyListingsURL(ctx context.Context, q Querier, companyID int64) error {
	var id int64
	err := q.QueryRowContext(ctx, `
UPDATE companies
   SET listings_url                  = NULL,
       listings_url_resolved_at      = NULL,
       listings_url_remote_confirmed = NULL,
       updated_at                    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id = ?
RETURNING id`, companyID).Scan(&id)
	if err != nil {
		return fmt.Errorf("clear listings url for company %d: %w", companyID, err)
	}
	return nil
}

// CompanySlugByID returns the slug for a company id, or sql.ErrNoRows.
func CompanySlugByID(ctx context.Context, q Querier, id int64) (string, error) {
	var slug string
	if err := q.QueryRowContext(ctx, `SELECT slug FROM companies WHERE id = ?`, id).Scan(&slug); err != nil {
		return "", err
	}
	return slug, nil
}

// VendorNameForCompany returns the classified ATS vendor name for a company, or sql.ErrNoRows when
// the company has not been classified yet (no company_application_platforms row).
func VendorNameForCompany(ctx context.Context, q Querier, companyID int64) (string, error) {
	var name string
	err := q.QueryRowContext(ctx, `
SELECT p.name
  FROM company_application_platforms cap
  JOIN platforms p ON p.id = cap.platform_id
 WHERE cap.company_id = ?`, companyID).Scan(&name)
	if err != nil {
		return "", err
	}
	return name, nil
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
