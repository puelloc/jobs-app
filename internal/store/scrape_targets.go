// scrape_targets.go: the read query that feeds a full-sweep batch runner.
package store

import (
	"context"
	"fmt"
)

// ScrapeTarget is one company ready for a listings scrape: it has a stored careers URL (where the
// agent starts) and a classified vendor (how the flow routes and keys postings).
type ScrapeTarget struct {
	CompanyID int64
	Slug      string
	Name      string
	Vendor    string
}

// ListScrapeTargets returns the companies eligible for the listings scrape, ordered by name. A
// company is eligible when it has a non-empty career_site_url and a classified application
// platform. limit <= 0 means no cap.
func ListScrapeTargets(ctx context.Context, q Querier, limit int) ([]ScrapeTarget, error) {
	query := `
SELECT c.id, c.slug, c.name, p.name
  FROM companies c
  JOIN company_application_platforms cap ON cap.company_id = c.id
  JOIN platforms p ON p.id = cap.platform_id
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
		var t ScrapeTarget
		if err := rows.Scan(&t.CompanyID, &t.Slug, &t.Name, &t.Vendor); err != nil {
			return nil, fmt.Errorf("scan scrape target: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scrape targets: %w", err)
	}
	return out, nil
}
