// company_reads.go: read queries for the company directory view.
//
// The companies table is written by two different pipelines - the index bootstrap and the careers
// resolver - so this is the one place both are read together. Nothing here formats: the UI owns
// presentation, exactly as it does for jobs.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CompanyRow is one companies row as the directory view needs it. Nullable columns use sql.Null*
// so the API layer decides how to present "not known".
type CompanyRow struct {
	ID               int64
	Slug             string
	Name             string
	Industry         sql.NullString
	SubIndustry      sql.NullString
	Headquarters     sql.NullString
	IndexMembership  sql.NullString
	Website          sql.NullString
	WebsiteSource    sql.NullString
	CareerSiteURL    sql.NullString
	CareerSiteSource sql.NullString
	// CareerSiteTitle is the document title of the accepted careers page, which is what makes a
	// wrong pick visible without opening the URL.
	CareerSiteTitle sql.NullString
	// AttemptCount is how many resolution attempts exist across all runs, so an unresolved company
	// can be told apart from one that has never been looked at.
	AttemptCount int64
	UpdatedAt    string
}

// CompanyFilter selects a subset of the directory.
type CompanyFilter struct {
	// IndexMembership restricts to 'sp500', 'sp400' or 'sp600'. Empty means all.
	IndexMembership string
	// Resolution restricts by whether a careers site has been found: "resolved", "unresolved", or
	// "unattempted". Empty means all.
	Resolution string
	// Search matches the name or slug, case-insensitively. Empty means no filtering.
	Search string
}

// Resolution filter values.
const (
	ResolutionResolved    = "resolved"
	ResolutionUnresolved  = "unresolved"
	ResolutionUnattempted = "unattempted"
)

// Index memberships, matching companies.index_membership.
var validMemberships = map[string]bool{"sp500": true, "sp400": true, "sp600": true}

// Validate reports whether the filter's values are ones this query understands.
//
// Validation lives here rather than in the handler so an unrecognised value cannot reach the SQL as
// a silent no-op: a typo in ?resolution=resovled would otherwise return every company, which reads
// as "everything is unresolved" rather than as a mistake.
func (f CompanyFilter) Validate() error {
	if f.IndexMembership != "" && !validMemberships[f.IndexMembership] {
		return fmt.Errorf("index must be one of sp500, sp400, sp600; got %q", f.IndexMembership)
	}
	switch f.Resolution {
	case "", ResolutionResolved, ResolutionUnresolved, ResolutionUnattempted:
		return nil
	default:
		return fmt.Errorf("resolution must be resolved, unresolved or unattempted; got %q", f.Resolution)
	}
}

// ListCompanies returns one page of companies plus the total matching the filter.
func ListCompanies(ctx context.Context, q Querier, filter CompanyFilter, limit, offset int) ([]CompanyRow, int64, error) {
	if err := filter.Validate(); err != nil {
		return nil, 0, err
	}

	where, args := companyWhere(filter)

	var total int64
	countQuery := `SELECT count(*) FROM companies c` + where
	if err := q.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count companies: %w", err)
	}

	listQuery := `
SELECT c.id, c.slug, c.name, c.industry, c.gics_sub_industry, c.headquarters_location,
       c.index_membership, c.website, c.website_source,
       c.career_site_url, c.career_site_url_source, c.career_site_url_title,
       (SELECT count(*) FROM url_resolution_attempts a WHERE a.company_id = c.id) AS attempt_count,
       c.updated_at
  FROM companies c` + where + `
 ORDER BY c.name COLLATE NOCASE, c.id
 LIMIT ? OFFSET ?`

	rows, err := q.QueryContext(ctx, listQuery, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list companies: %w", err)
	}
	defer rows.Close()

	var out []CompanyRow
	for rows.Next() {
		var c CompanyRow
		if err := rows.Scan(
			&c.ID, &c.Slug, &c.Name, &c.Industry, &c.SubIndustry, &c.Headquarters,
			&c.IndexMembership, &c.Website, &c.WebsiteSource,
			&c.CareerSiteURL, &c.CareerSiteSource, &c.CareerSiteTitle,
			&c.AttemptCount, &c.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan company: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate companies: %w", err)
	}
	return out, total, nil
}

// companyWhere builds the WHERE clause and its arguments.
//
// A company with no attempts is "unattempted" rather than merely unresolved, which is the
// distinction an operator needs: unresolved after a pass is a result, never-looked-at is a gap.
func companyWhere(f CompanyFilter) (string, []any) {
	var clauses []string
	var args []any

	if f.IndexMembership != "" {
		clauses = append(clauses, "c.index_membership = ?")
		args = append(args, f.IndexMembership)
	}
	switch f.Resolution {
	case ResolutionResolved:
		clauses = append(clauses, "c.career_site_url IS NOT NULL")
	case ResolutionUnresolved:
		clauses = append(clauses, "c.career_site_url IS NULL",
			"EXISTS (SELECT 1 FROM url_resolution_attempts a WHERE a.company_id = c.id)")
	case ResolutionUnattempted:
		clauses = append(clauses, "c.career_site_url IS NULL",
			"NOT EXISTS (SELECT 1 FROM url_resolution_attempts a WHERE a.company_id = c.id)")
	}
	if search := strings.TrimSpace(f.Search); search != "" {
		// A LIKE over name and slug. The escape character is declared so a user-supplied % or _
		// is matched literally rather than silently widening the result set.
		clauses = append(clauses, `(c.name LIKE ? ESCAPE '\' OR c.slug LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(search) + "%"
		args = append(args, pattern, pattern)
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// escapeLike neutralises LIKE wildcards in user input.
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return replacer.Replace(s)
}

// AttemptRow is one url_resolution_attempts row for a company's detail view.
type AttemptRow struct {
	ID              int64
	RunID           sql.NullInt64
	AttemptIndex    int64
	Source          string
	CandidateURL    string
	Kind            string
	HTTPStatus      sql.NullInt64
	FinalURL        sql.NullString
	Title           sql.NullString
	ValidationState string
	RejectionReason sql.NullString
	EvidencePath    sql.NullString
	CreatedAt       string
}

// ListCompanyAttempts returns every resolution attempt for a company, newest run first.
//
// The whole trail is returned rather than only the winning row, because a wrong pick is only
// diagnosable alongside the candidates that were refused before it.
func ListCompanyAttempts(ctx context.Context, q Querier, companyID int64) ([]AttemptRow, error) {
	const query = `
SELECT id, run_id, attempt_index, source, candidate_url, candidate_kind,
       http_status, final_url, title, validation_status, rejection_reason, evidence_path, created_at
  FROM url_resolution_attempts
 WHERE company_id = ?
 ORDER BY run_id DESC, attempt_index ASC, id ASC`

	rows, err := q.QueryContext(ctx, query, companyID)
	if err != nil {
		return nil, fmt.Errorf("list attempts for company %d: %w", companyID, err)
	}
	defer rows.Close()

	var out []AttemptRow
	for rows.Next() {
		var a AttemptRow
		if err := rows.Scan(
			&a.ID, &a.RunID, &a.AttemptIndex, &a.Source, &a.CandidateURL, &a.Kind,
			&a.HTTPStatus, &a.FinalURL, &a.Title, &a.ValidationState,
			&a.RejectionReason, &a.EvidencePath, &a.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempts: %w", err)
	}
	return out, nil
}

// GetCompany returns one company by id.
func GetCompany(ctx context.Context, q Querier, id int64) (CompanyRow, error) {
	const query = `
SELECT c.id, c.slug, c.name, c.industry, c.gics_sub_industry, c.headquarters_location,
       c.index_membership, c.website, c.website_source,
       c.career_site_url, c.career_site_url_source, c.career_site_url_title,
       (SELECT count(*) FROM url_resolution_attempts a WHERE a.company_id = c.id),
       c.updated_at
  FROM companies c
 WHERE c.id = ?`

	var c CompanyRow
	err := q.QueryRowContext(ctx, query, id).Scan(
		&c.ID, &c.Slug, &c.Name, &c.Industry, &c.SubIndustry, &c.Headquarters,
		&c.IndexMembership, &c.Website, &c.WebsiteSource,
		&c.CareerSiteURL, &c.CareerSiteSource, &c.CareerSiteTitle,
		&c.AttemptCount, &c.UpdatedAt,
	)
	if err != nil {
		return CompanyRow{}, err
	}
	return c, nil
}
