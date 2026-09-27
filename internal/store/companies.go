// companies.go: resolve a source's free-text company string to a companies row,
// creating it on first sight.
//
// design: docs/remoteok-mapping.md, Column mapping - `company_id` is resolved
// from the `company` string; the payload carries no company identifier.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// insertCompanySQL inserts a company unless its slug already exists, in which
// case it returns no row at all (that absence is how the caller detects an
// existing company). It goes through QueryRowContext because the Querier
// interface established in job_reads.go exposes only the query methods, and
// INSERT ... RETURNING is the same technique runs.go already uses.
const insertCompanySQL = `
INSERT INTO companies (slug, name)
VALUES (?, ?)
ON CONFLICT(slug) DO NOTHING
RETURNING id`

const selectCompanyBySlugSQL = `SELECT id FROM companies WHERE slug = ?`

// ResolveOrCreateCompany returns the id of the companies row for name, creating
// the row if this is the first time the name has been seen.
//
// The lookup key is a slug derived from name, not the name itself: the source's
// company strings vary only cosmetically ("Bjak " with a trailing space) and the
// schema's unique constraint is on slug. companies.name keeps the first spelling
// that was stored, because ON CONFLICT DO NOTHING does not touch it.
//
// The caller supplies the Querier, so this participates in whatever transaction
// it is given (main.go passes the one *sql.Tx that also carries the upserts and
// the stale-marking).
func ResolveOrCreateCompany(ctx context.Context, q Querier, name string) (int64, error) {
	slug := slugifyCompany(name)
	if slug == "" {
		// Naming the input is safe: it is the source's own company string on a
		// row that cannot be attributed, and there is no other way to report it.
		return 0, fmt.Errorf("resolve company %q: name has no characters usable in a slug", name)
	}

	var id int64
	err := q.QueryRowContext(ctx, insertCompanySQL, slug, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("resolve company %q (slug %q): insert: %w", name, slug, err)
	}

	// DO NOTHING returned no row, so this slug already exists.
	if err := q.QueryRowContext(ctx, selectCompanyBySlugSQL, slug).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve company %q (slug %q): select existing: %w", name, slug, err)
	}
	return id, nil
}

// slugifyCompany normalizes a company name into the stable key used for
// dedupe: trim, lowercase, collapse internal whitespace to single hyphens, drop
// everything that is not [a-z0-9-], collapse repeated hyphens, and trim leading
// and trailing hyphens.
//
// Whitespace and other punctuation become a hyphen separator rather than being
// deleted, so "Balco, Inc." slugs as "balco-inc" instead of "balcoinc" and the
// document's near-collisions stay legible. docs/remoteok-mapping.md records no
// slug collision among the 91 distinct company strings in the sample.
func slugifyCompany(name string) string {
	const hyphen = '-'

	var b strings.Builder
	b.Grow(len(name))
	lastWasHyphen := false

	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastWasHyphen = false
		default:
			// Any separator (whitespace, punctuation) collapses to one hyphen,
			// and never leads the slug.
			if !lastWasHyphen && b.Len() > 0 {
				b.WriteRune(hyphen)
				lastWasHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
