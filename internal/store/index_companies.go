// index_companies.go: persist S&P index constituents into companies.
//
// design: docs/sp1500-plan.md, sections 5.1 and 6. The three constituent pages are an identity
// source only - they say who a company is, not whether it hires. The enrichment columns here exist
// so the careers-resolution step has somewhere to record what it finds.
package store

import (
	"context"
	"fmt"
	"strings"
)

// Index names, matching companies.index_membership.
const (
	IndexSP500 = "sp500"
	IndexSP400 = "sp400"
	IndexSP600 = "sp600"
)

// IndexCompany is one constituent row from an S&P index page.
//
// It is a plain struct rather than the parser's own type so this package does not depend on
// internal/sp1500; the caller maps between them.
type IndexCompany struct {
	Name         string
	Article      string
	Industry     string
	SubIndustry  string
	Headquarters string
	CIK          int64
	RawCIK       string
}

// UpsertIndexCompanies writes one index page's constituents and returns how many were newly created
// rather than updated.
//
// The dedupe key is a slug derived from the company name, not the name itself, because the same
// company appears on two rows when it has two share classes: Alphabet is listed as GOOGL and GOOG,
// and both rows carry the same name. Those collapse to one companies row here, which is why the
// returned count is smaller than the row count.
//
// Companies first seen via another index keep their row and have index_membership overwritten: the
// three lists are disjoint, so membership is a current fact rather than a set.
//
// A name with no characters usable in a slug is reported rather than silently skipped or stored
// with a NULL key, because a NULL key would defeat the unique constraint and duplicate on every run.
func UpsertIndexCompanies(ctx context.Context, q Querier, index string, companies []IndexCompany) (inserted int, err error) {
	seen := make(map[string]struct{}, len(companies))
	for i, c := range companies {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return inserted, fmt.Errorf("index %s row %d: empty company name", index, i)
		}
		slug := slugifyCompany(name)
		if slug == "" {
			return inserted, fmt.Errorf("index %s row %d: company %q has no characters usable in a slug", index, i, name)
		}
		if _, dup := seen[slug]; dup {
			// A second share class of a company already written in this run. The row is
			// deliberately not counted as a second company.
			continue
		}
		created, err := upsertIndexCompany(ctx, q, slug, index, c)
		if err != nil {
			return inserted, err
		}
		seen[slug] = struct{}{}
		if created {
			inserted++
		}
	}
	return inserted, nil
}

// upsertIndexCompany writes one company and reports whether it created a new row.
//
// It is a single statement, so a repeated or concurrent run cannot observe a half-written row, and
// created_at is left alone on an update.
//
// created_at has a DDL default but no trigger maintains updated_at - the schema deliberately leaves
// that to application code - so the update branch sets it explicitly. The DO UPDATE clause carries
// a WHERE that is false for a row whose values already match, which makes RETURNING yield no row;
// an empty result is therefore how "this company already existed" is detected.
func upsertIndexCompany(ctx context.Context, q Querier, slug, index string, c IndexCompany) (created bool, err error) {
	const query = `
INSERT INTO companies (
    slug, name, industry, cik, gics_sub_industry, headquarters_location,
    index_membership, website, website_source
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL)
ON CONFLICT(slug) DO UPDATE SET
    name                  = excluded.name,
    industry              = excluded.industry,
    cik                   = excluded.cik,
    gics_sub_industry     = excluded.gics_sub_industry,
    headquarters_location = excluded.headquarters_location,
    index_membership      = excluded.index_membership,
    updated_at            = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE companies.name                  IS NOT excluded.name
   OR companies.industry              IS NOT excluded.industry
   OR companies.cik                   IS NOT excluded.cik
   OR companies.gics_sub_industry     IS NOT excluded.gics_sub_industry
   OR companies.headquarters_location IS NOT excluded.headquarters_location
   OR companies.index_membership      IS NOT excluded.index_membership
RETURNING id`

	// cik is stored as text: the S&P 400 page has no CIK column and its CIK= values are
	// ticker-like for most rows, so an integer column would have to invent a value.
	var cik any
	switch {
	case c.CIK != 0:
		cik = fmt.Sprintf("%d", c.CIK)
	case strings.TrimSpace(c.RawCIK) != "":
		cik = strings.TrimSpace(c.RawCIK)
	}

	// The statement is issued through QueryContext because Querier exposes only the query
	// methods, and the RETURNING rows must be consumed for the statement to have run.
	// Empty strings are stored as NULL so "not known" has one representation rather than two.
	rows, err := q.QueryContext(ctx, query,
		slug, c.Name, nullable(c.Industry), cik, nullable(c.SubIndustry), nullable(c.Headquarters), index)
	if err != nil {
		return false, fmt.Errorf("upsert company %q (slug %q): %w", c.Name, slug, err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return false, fmt.Errorf("upsert company %q (slug %q): scan: %w", c.Name, slug, err)
		}
		created = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("upsert company %q (slug %q): %w", c.Name, slug, err)
	}
	return created, nil
}

// nullable maps an empty string to NULL, so an unknown value has a single representation. The
// parser reports an empty GICS Sub-Industry or Headquarters for some rows, and two spellings of
// "unknown" would make later queries need both.
func nullable(s string) any {
	if trimmed := strings.TrimSpace(s); trimmed != "" {
		return trimmed
	}
	return nil
}
