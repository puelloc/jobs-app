// scrape_targets.go: the read query that feeds a full-sweep batch runner.
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ScrapeTarget is one company ready for a listings scrape: it has a stored careers URL (where the
// agent starts) and a classified vendor (how the flow routes and keys postings). LastRunID and
// LastRunStatus describe the company's most recent listings scrape, or are nil when it has never
// been scraped; the batch runner uses them to skip companies that already worked.
type ScrapeTarget struct {
	CompanyID     int64
	Slug          string
	Name          string
	Vendor        string
	LastRunID     *int64
	LastRunStatus *string
}

// ListScrapeTargets returns the companies eligible for the listings scrape, ordered by name. A
// company is eligible when it has a non-empty career_site_url and a classified application
// platform. limit <= 0 means no cap.
//
// The correlated subquery fetches each company's newest listings run (the run whose company_id
// matches, ordered by started_at then id) so the caller can skip a company whose latest attempt
// already succeeded or already left a browser-use trace. NULL columns mean "never scraped".
func ListScrapeTargets(ctx context.Context, q Querier, limit int) ([]ScrapeTarget, error) {
	query := `
SELECT c.id, c.slug, c.name, p.name, lr.id, lr.status
  FROM companies c
  JOIN company_application_platforms cap ON cap.company_id = c.id
  JOIN platforms p ON p.id = cap.platform_id
  LEFT JOIN scrape_runs lr ON lr.id = (
        SELECT r2.id
          FROM scrape_runs r2
         WHERE r2.company_id = c.id
         ORDER BY r2.started_at DESC, r2.id DESC
         LIMIT 1)
 WHERE c.career_site_url IS NOT NULL AND c.career_site_url <> ''
 ORDER BY c.name COLLATE NOCASE, c.id`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list scrape targets: %w", err)
	}
	defer rows.Close()

	var out []ScrapeTarget
	for rows.Next() {
		var (
			t             ScrapeTarget
			lastRunID     sql.NullInt64
			lastRunStatus sql.NullString
		)
		if err := rows.Scan(&t.CompanyID, &t.Slug, &t.Name, &t.Vendor, &lastRunID, &lastRunStatus); err != nil {
			return nil, fmt.Errorf("scan scrape target: %w", err)
		}
		if lastRunID.Valid {
			v := lastRunID.Int64
			t.LastRunID = &v
		}
		if lastRunStatus.Valid {
			s := lastRunStatus.String
			t.LastRunStatus = &s
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scrape targets: %w", err)
	}
	return out, nil
}
