// platforms.go: record a company's applicant-tracking-system classification.
package store

import (
	"context"
	"fmt"
)

// SetCompanyApplicationPlatform records that companyID runs its careers board on platformID, with
// the board's tenant URL. It enforces the schema invariant that platformID references an
// 'application_system' platform, and is idempotent: re-classifying the same (company, platform) pair
// updates base_url rather than duplicating the row.
func SetCompanyApplicationPlatform(ctx context.Context, q Querier, companyID, platformID int64, baseURL string) error {
	var platformType string
	if err := q.QueryRowContext(ctx, `SELECT platform_type FROM platforms WHERE id = ?`, platformID).Scan(&platformType); err != nil {
		return fmt.Errorf("read platform %d: %w", platformID, err)
	}
	if platformType != "application_system" {
		return fmt.Errorf("platform %d is %q, want application_system", platformID, platformType)
	}

	const query = `
INSERT INTO company_application_platforms (company_id, platform_id, base_url)
VALUES (?, ?, ?)
ON CONFLICT(company_id, platform_id) DO UPDATE SET base_url = excluded.base_url
RETURNING id`
	var id int64
	if err := q.QueryRowContext(ctx, query, companyID, platformID, nullable(baseURL)).Scan(&id); err != nil {
		return fmt.Errorf("set application platform %d for company %d: %w", platformID, companyID, err)
	}
	return nil
}
