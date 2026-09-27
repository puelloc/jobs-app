// churn_reads.go: which companies changed their careers URL between runs.
//
// The resolver is idempotent and is meant to be re-run weekly, so a company can be resolved in more
// than one run. The interesting fact is then not the current URL but a change in it: a company that
// resolved to one careers page in run 4 and a different one in run 5 is either a site that moved or
// a gate whose mind changed, and both are worth a look.
//
// The signal is two accepted careers-kind attempts for one company in different runs whose
// final_url differs. Ordering is by run_id, which is the run table's rowid and therefore
// monotonic in time.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ChurnRow is one company whose accepted careers URL changed from one run to the next.
type ChurnRow struct {
	CompanyID int64
	Slug      string
	Name      string

	FromRunID int64
	FromURL   string
	FromTitle sql.NullString
	FromAt    string

	ToRunID int64
	ToURL   string
	ToTitle sql.NullString
	ToAt    string
}

// churnAcceptedCTE collapses the attempts table to one accepted careers URL per (company, run) and
// then compares each run with the company's previous one.
//
// Three restrictions are load-bearing:
//
//   - candidate_kind is career_site or ats_board only. A website attempt is the homepage the careers
//     tiers run against, not a careers answer; comparing homepages would report churn whenever a
//     homepage's redirect target moved.
//   - final_url must be non-empty. The full run wrote 3,065 accepted robots_sitemap rows with an
//     empty final_url - the inputs the tier used to build candidates, recorded as though they were
//     answers. They are not answers and would otherwise read as a change to "".
//   - run_id must be non-null. Rows written outside a run have no position in the sequence.
//
// ROW_NUMBER picks one attempt per (company, run) rather than assuming there is only one; no current
// company/run has more than one distinct accepted careers URL, but the query should not depend on
// that staying true. The latest attempt index wins, matching what the writer stored.
const churnAcceptedCTE = `
WITH accepted AS (
    SELECT a.company_id, a.run_id, a.final_url, a.title, a.created_at,
           ROW_NUMBER() OVER (
               PARTITION BY a.company_id, a.run_id
               ORDER BY a.attempt_index DESC, a.id DESC
           ) AS rn
      FROM url_resolution_attempts a
     WHERE a.validation_status = 'accepted'
       AND a.candidate_kind IN ('career_site','ats_board')
       AND a.final_url IS NOT NULL AND a.final_url <> ''
       AND a.run_id IS NOT NULL
),
per_run AS (
    SELECT company_id, run_id, final_url, title, created_at FROM accepted WHERE rn = 1
),
compared AS (
    SELECT company_id, run_id, final_url, title, created_at,
           LAG(run_id)     OVER (PARTITION BY company_id ORDER BY run_id) AS prev_run_id,
           LAG(final_url)  OVER (PARTITION BY company_id ORDER BY run_id) AS prev_url,
           LAG(title)      OVER (PARTITION BY company_id ORDER BY run_id) AS prev_title,
           LAG(created_at) OVER (PARTITION BY company_id ORDER BY run_id) AS prev_at
      FROM per_run
)
`

// churnPredicate is the transition test shared by the count and list queries.
const churnPredicate = `
 WHERE cmp.prev_url IS NOT NULL AND cmp.prev_url <> cmp.final_url`

// ListResolutionChurn returns one page of careers-URL changes, newest run first, plus the total
// number of changes matching the filter.
//
// The filter's Resolution field is ignored: every row here is by definition resolved. Index and
// search are honoured, and Validate still runs so an unrecognised index is a 400 rather than a
// silent full table.
func ListResolutionChurn(ctx context.Context, q Querier, filter CompanyFilter, limit, offset int) ([]ChurnRow, int64, error) {
	if err := filter.Validate(); err != nil {
		return nil, 0, err
	}

	where, args := churnWhere(filter)

	var total int64
	countQuery := churnAcceptedCTE + `
SELECT count(*)
  FROM compared cmp
  JOIN companies c ON c.id = cmp.company_id` + churnPredicate + where
	if err := q.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count resolution churn: %w", err)
	}

	listQuery := churnAcceptedCTE + `
SELECT c.id, c.slug, c.name,
       cmp.prev_run_id, cmp.prev_url, cmp.prev_title, cmp.prev_at,
       cmp.run_id, cmp.final_url, cmp.title, cmp.created_at
  FROM compared cmp
  JOIN companies c ON c.id = cmp.company_id` + churnPredicate + where + `
 ORDER BY cmp.run_id DESC, c.name COLLATE NOCASE, c.id
 LIMIT ? OFFSET ?`

	rows, err := q.QueryContext(ctx, listQuery, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list resolution churn: %w", err)
	}
	defer rows.Close()

	var out []ChurnRow
	for rows.Next() {
		var r ChurnRow
		if err := rows.Scan(
			&r.CompanyID, &r.Slug, &r.Name,
			&r.FromRunID, &r.FromURL, &r.FromTitle, &r.FromAt,
			&r.ToRunID, &r.ToURL, &r.ToTitle, &r.ToAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan resolution churn: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate resolution churn: %w", err)
	}
	return out, total, nil
}

// churnWhere builds the extra clauses for the churn query. It uses AND, not WHERE, because the
// transition predicate is already the base query's WHERE.
func churnWhere(f CompanyFilter) (string, []any) {
	var clauses []string
	var args []any

	if f.IndexMembership != "" {
		clauses = append(clauses, "c.index_membership = ?")
		args = append(args, f.IndexMembership)
	}
	if search := strings.TrimSpace(f.Search); search != "" {
		clauses = append(clauses, `(c.name LIKE ? ESCAPE '\' OR c.slug LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(search) + "%"
		args = append(args, pattern, pattern)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
}
