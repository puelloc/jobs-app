// index_companies_test.go: tests for persisting S&P index constituents.
package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func upsertTestIndex(t *testing.T, database *sql.DB, index string, companies []IndexCompany) int {
	t.Helper()
	inserted, err := UpsertIndexCompanies(context.Background(), database, index, companies)
	if err != nil {
		t.Fatalf("UpsertIndexCompanies: %v", err)
	}
	return inserted
}

func TestUpsertIndexCompaniesStoresEveryEnrichmentColumn(t *testing.T) {
	database := newTestDB(t)

	inserted := upsertTestIndex(t, database, IndexSP500, []IndexCompany{{
		Name:         "Apple Inc.",
		Article:      "Apple Inc.",
		Industry:     "Information Technology",
		SubIndustry:  "Technology Hardware, Storage & Peripherals",
		Headquarters: "Cupertino, California",
		CIK:          320193,
		RawCIK:       "0000320193",
	}})
	if inserted != 1 {
		t.Errorf("inserted = %d, want 1", inserted)
	}

	var (
		slug, name, industry, subIndustry, hq, membership, cik string
	)
	if err := database.QueryRow(`
SELECT slug, name, industry, gics_sub_industry, headquarters_location, index_membership, cik
FROM companies WHERE slug = 'apple-inc'`).
		Scan(&slug, &name, &industry, &subIndustry, &hq, &membership, &cik); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "Apple Inc." {
		t.Errorf("name = %q", name)
	}
	if industry != "Information Technology" {
		t.Errorf("industry = %q", industry)
	}
	if subIndustry != "Technology Hardware, Storage & Peripherals" {
		t.Errorf("gics_sub_industry = %q", subIndustry)
	}
	if hq != "Cupertino, California" {
		t.Errorf("headquarters_location = %q", hq)
	}
	if membership != IndexSP500 {
		t.Errorf("index_membership = %q, want %q", membership, IndexSP500)
	}
	if cik != "320193" {
		t.Errorf("cik = %q, want the numeric form", cik)
	}
}

// dual-class: Alphabet is listed as GOOGL and GOOG on two rows with the same name. They must
// collapse to one company rather than two.
func TestUpsertIndexCompaniesDedupesShareClassesBySlug(t *testing.T) {
	database := newTestDB(t)

	inserted := upsertTestIndex(t, database, IndexSP500, []IndexCompany{
		{Name: "Alphabet Inc.", Industry: "Communication Services", CIK: 1652044},
		{Name: "Alphabet Inc.", Industry: "Communication Services", CIK: 1652044},
	})
	if inserted != 1 {
		t.Errorf("inserted = %d, want 1 (both share classes are one company)", inserted)
	}

	var n int
	if err := database.QueryRow(`SELECT count(*) FROM companies WHERE slug = 'alphabet-inc'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("companies rows for alphabet-inc = %d, want 1", n)
	}
}

// The second run of the same page must create nothing and must not duplicate.
func TestUpsertIndexCompaniesIsIdempotent(t *testing.T) {
	database := newTestDB(t)
	rows := []IndexCompany{
		{Name: "Apple Inc.", Industry: "Information Technology", CIK: 320193},
		{Name: "3M", Industry: "Industrials", CIK: 66740},
	}

	first := upsertTestIndex(t, database, IndexSP500, rows)
	if first != 2 {
		t.Fatalf("first run inserted = %d, want 2", first)
	}
	second := upsertTestIndex(t, database, IndexSP500, rows)
	if second != 0 {
		t.Errorf("second run inserted = %d, want 0", second)
	}

	var n int
	if err := database.QueryRow(`SELECT count(*) FROM companies`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("companies rows = %d, want 2", n)
	}
}

// Re-observation is authoritative: a changed industry is written.
func TestUpsertIndexCompaniesUpdatesChangedValues(t *testing.T) {
	database := newTestDB(t)

	upsertTestIndex(t, database, IndexSP500, []IndexCompany{
		{Name: "Acme Corp", Industry: "Industrials", Headquarters: "Boston, Massachusetts"},
	})
	upsertTestIndex(t, database, IndexSP500, []IndexCompany{
		{Name: "Acme Corp", Industry: "Information Technology", Headquarters: "Austin, Texas"},
	})

	var industry, hq string
	if err := database.QueryRow(`SELECT industry, headquarters_location FROM companies WHERE slug = 'acme-corp'`).
		Scan(&industry, &hq); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if industry != "Information Technology" {
		t.Errorf("industry = %q, want the re-observed value", industry)
	}
	if hq != "Austin, Texas" {
		t.Errorf("headquarters_location = %q, want the re-observed value", hq)
	}
}

// created_at is immutable across an upsert; updated_at is not.
func TestUpsertIndexCompaniesPreservesCreatedAt(t *testing.T) {
	database := newTestDB(t)

	upsertTestIndex(t, database, IndexSP500, []IndexCompany{{Name: "Acme Corp", Industry: "Industrials"}})
	var createdBefore string
	if err := database.QueryRow(`SELECT created_at FROM companies WHERE slug = 'acme-corp'`).Scan(&createdBefore); err != nil {
		t.Fatalf("read created_at: %v", err)
	}

	upsertTestIndex(t, database, IndexSP500, []IndexCompany{{Name: "Acme Corp", Industry: "Materials"}})

	var createdAfter string
	if err := database.QueryRow(`SELECT created_at FROM companies WHERE slug = 'acme-corp'`).Scan(&createdAfter); err != nil {
		t.Fatalf("read created_at after: %v", err)
	}
	if createdAfter != createdBefore {
		t.Errorf("created_at changed from %q to %q on an update", createdBefore, createdAfter)
	}
}

// The S&P 400 page states CIK as a ticker for most rows. It must be stored verbatim rather than
// coerced, because resolving it needs SEC company_tickers.json.
func TestUpsertIndexCompaniesStoresTickerLikeCikVerbatim(t *testing.T) {
	database := newTestDB(t)

	upsertTestIndex(t, database, IndexSP400, []IndexCompany{
		{Name: "Darling Ingredients", Industry: "Consumer Staples", RawCIK: "DAR"},
		{Name: "Alcoa", Industry: "Materials", RawCIK: "AA"},
	})

	for slug, want := range map[string]string{
		"darling-ingredients": "DAR",
		"alcoa":               "AA",
	} {
		var cik string
		if err := database.QueryRow(`SELECT cik FROM companies WHERE slug = ?`, slug).Scan(&cik); err != nil {
			t.Fatalf("read cik for %s: %v", slug, err)
		}
		if cik != want {
			t.Errorf("%s cik = %q, want %q", slug, cik, want)
		}
	}
}

// A numeric CIK wins over a ticker-like one when both are present.
func TestUpsertIndexCompaniesPrefersNumericCik(t *testing.T) {
	database := newTestDB(t)

	upsertTestIndex(t, database, IndexSP500, []IndexCompany{
		{Name: "Apple Inc.", Industry: "Information Technology", CIK: 320193, RawCIK: "AAPL"},
	})

	var cik string
	if err := database.QueryRow(`SELECT cik FROM companies WHERE slug = 'apple-inc'`).Scan(&cik); err != nil {
		t.Fatalf("read cik: %v", err)
	}
	if cik != "320193" {
		t.Errorf("cik = %q, want the numeric form 320193", cik)
	}
}

// An unknown value has one representation. The parser reports an empty GICS Sub-Industry or
// Headquarters for some rows, and storing "" as well as NULL would make every later query need both.
func TestUpsertIndexCompaniesStoresEmptyEnrichmentColumnsAsNull(t *testing.T) {
	database := newTestDB(t)

	upsertTestIndex(t, database, IndexSP400, []IndexCompany{
		{Name: "Darling Ingredients", Industry: "Consumer Staples", SubIndustry: "", Headquarters: "", RawCIK: "DAR"},
	})

	var sub, hq sql.NullString
	if err := database.QueryRow(`SELECT gics_sub_industry, headquarters_location FROM companies WHERE slug = 'darling-ingredients'`).
		Scan(&sub, &hq); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if sub.Valid {
		t.Errorf("gics_sub_industry = %q, want NULL for an unknown value", sub.String)
	}
	if hq.Valid {
		t.Errorf("headquarters_location = %q, want NULL for an unknown value", hq.String)
	}
}

// A move between indices updates membership rather than creating a second row.
func TestUpsertIndexCompaniesMovesMembership(t *testing.T) {
	database := newTestDB(t)

	upsertTestIndex(t, database, IndexSP400, []IndexCompany{{Name: "Acme Corp", Industry: "Industrials"}})
	upsertTestIndex(t, database, IndexSP500, []IndexCompany{{Name: "Acme Corp", Industry: "Industrials"}})

	var membership string
	if err := database.QueryRow(`SELECT index_membership FROM companies WHERE slug = 'acme-corp'`).Scan(&membership); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if membership != IndexSP500 {
		t.Errorf("index_membership = %q, want %q", membership, IndexSP500)
	}

	var n int
	if err := database.QueryRow(`SELECT count(*) FROM companies`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("companies rows = %d, want 1", n)
	}
}

// The wiki article title is carried through the parser but has no column yet. It is the join key
// M2 uses against Wikidata, so this test records that the gap is deliberate rather than an
// oversight: when a column is added, this test is what will change.
func TestUpsertIndexCompaniesDoesNotPersistTheArticleTitle(t *testing.T) {
	database := newTestDB(t)

	upsertTestIndex(t, database, IndexSP500, []IndexCompany{
		{Name: "Apple Inc.", Article: "Apple Inc.", Industry: "Information Technology"},
	})

	// No column holds it, so nothing should silently claim to.
	rows, err := database.Query(`SELECT name FROM pragma_table_info('companies')`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if strings.Contains(strings.ToLower(col), "article") {
			t.Fatalf("companies gained a column %q holding the article title; persist it in the upsert too", col)
		}
	}
}

// A row with no usable slug is reported, not written with a NULL key: a NULL key would defeat the
// unique constraint and duplicate on the next run.
func TestUpsertIndexCompaniesRejectsEmptyName(t *testing.T) {
	database := newTestDB(t)

	if _, err := UpsertIndexCompanies(context.Background(), database, IndexSP500, []IndexCompany{{Name: "   "}}); err == nil {
		t.Fatal("UpsertIndexCompanies accepted an empty name, want an error")
	}
	if _, err := UpsertIndexCompanies(context.Background(), database, IndexSP500, []IndexCompany{{Name: "!!!"}}); err == nil {
		t.Fatal("UpsertIndexCompanies accepted a name with no slug characters, want an error")
	}
}

// The enrichment columns start empty: website and its provenance belong to the careers-resolution
// step, which must not be able to mistake "not looked up yet" for "looked up and found nothing".
func TestUpsertIndexCompaniesLeavesWebsiteColumnsNull(t *testing.T) {
	database := newTestDB(t)

	upsertTestIndex(t, database, IndexSP500, []IndexCompany{{Name: "Apple Inc.", Industry: "Information Technology"}})

	var website, source sql.NullString
	if err := database.QueryRow(`SELECT website, website_source FROM companies WHERE slug = 'apple-inc'`).
		Scan(&website, &source); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if website.Valid || source.Valid {
		t.Errorf("website = %v, website_source = %v, want both NULL", website, source)
	}
}

// The shared slug helper is what makes dual-class rows collapse, so its behaviour is pinned here
// alongside the caller that depends on it.
func TestIndexCompanySlugCollapsesCosmeticVariation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Apple Inc.", "apple-inc"},
		{"Alphabet Inc.", "alphabet-inc"},
		{"3M", "3m"},
		{"AT&T", "at-t"},
		{"A. O. Smith", "a-o-smith"},
		{"  Spaced   Out  ", "spaced-out"},
	} {
		if got := slugifyCompany(tc.in); got != tc.want {
			t.Errorf("slugifyCompany(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
