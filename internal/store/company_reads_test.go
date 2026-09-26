// company_reads_test.go: tests for the company directory reads.
package store

import (
	"context"
	"database/sql"
	"testing"
)

func seedDirectoryCompany(t *testing.T, database *sql.DB, id int64, slug, name, membership string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name, industry, index_membership) VALUES (?, ?, ?, 'Industrials', ?)`,
		id, slug, name, membership); err != nil {
		t.Fatalf("seed %s: %v", slug, err)
	}
}

func TestListCompaniesReturnsEveryCompanyByDefault(t *testing.T) {
	database := newTestDB(t)
	seedDirectoryCompany(t, database, 1, "acme", "Acme Corporation", "sp500")
	seedDirectoryCompany(t, database, 2, "beta", "Beta Inc.", "sp600")

	rows, total, err := ListCompanies(context.Background(), database, CompanyFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("ListCompanies: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Errorf("total=%d rows=%d, want 2 and 2", total, len(rows))
	}
	// Ordered by name, case-insensitively.
	if rows[0].Name != "Acme Corporation" || rows[1].Name != "Beta Inc." {
		t.Errorf("order = %q, %q", rows[0].Name, rows[1].Name)
	}
	if rows[0].IndexMembership.String != "sp500" {
		t.Errorf("index_membership = %q", rows[0].IndexMembership.String)
	}
}

func TestListCompaniesFiltersByIndex(t *testing.T) {
	database := newTestDB(t)
	seedDirectoryCompany(t, database, 1, "acme", "Acme Corporation", "sp500")
	seedDirectoryCompany(t, database, 2, "beta", "Beta Inc.", "sp600")

	rows, total, err := ListCompanies(context.Background(), database, CompanyFilter{IndexMembership: "sp600"}, 50, 0)
	if err != nil {
		t.Fatalf("ListCompanies: %v", err)
	}
	if total != 1 || rows[0].Slug != "beta" {
		t.Errorf("total=%d rows=%v, want only beta", total, rows)
	}
}

// An unrecognised filter value must be refused, not silently ignored: a typo would otherwise return
// every company and read as a result rather than as a mistake.
func TestCompanyFilterRejectsUnknownValues(t *testing.T) {
	if err := (CompanyFilter{IndexMembership: "sp900"}).Validate(); err == nil {
		t.Error("an unknown index was accepted")
	}
	if err := (CompanyFilter{Resolution: "resovled"}).Validate(); err == nil {
		t.Error("a misspelled resolution was accepted")
	}
	for _, ok := range []CompanyFilter{
		{}, {IndexMembership: "sp500"}, {Resolution: ResolutionResolved},
		{Resolution: ResolutionUnresolved}, {Resolution: ResolutionUnattempted},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("valid filter %+v was refused: %v", ok, err)
		}
	}
}

// Attempted and never-attempted are different states, and the distinction is what tells an operator
// whether a resolution pass has covered a company at all.
func TestListCompaniesDistinguishesUnresolvedFromUnattempted(t *testing.T) {
	database := newTestDB(t)
	seedDirectoryCompany(t, database, 1, "tried", "Tried Corp", "sp500")
	seedDirectoryCompany(t, database, 2, "untouched", "Untouched Corp", "sp500")
	seedDirectoryCompany(t, database, 3, "solved", "Solved Corp", "sp500")

	runID, _, err := StartRun(context.Background(), database, 23)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	writer := NewResolutionWriter(database, "")
	if _, err := writer.Write(context.Background(), runID, 0, Resolution{
		CompanyID: 1, CompanySlug: "tried",
		Attempts: []ResolutionAttempt{{
			Source: "homepage", CandidateURL: "https://tried.example.com/", Kind: "website",
			ValidationState: "rejected", Reason: "forbidden",
		}},
	}); err != nil {
		t.Fatalf("write attempt: %v", err)
	}
	if _, err := writer.Write(context.Background(), runID, 0, Resolution{
		CompanyID: 3, CompanySlug: "solved",
		CareerSiteURL: "https://solved.example.com/careers", CareerSiteSource: CareerSiteSourceAnchorScan,
		Attempts: []ResolutionAttempt{{
			Source: "homepage", CandidateURL: "https://solved.example.com/", Kind: "career_site",
			ValidationState: "accepted",
		}},
	}); err != nil {
		t.Fatalf("write resolution: %v", err)
	}

	ctx := context.Background()

	resolved, total, err := ListCompanies(ctx, database, CompanyFilter{Resolution: ResolutionResolved}, 50, 0)
	if err != nil || total != 1 || resolved[0].Slug != "solved" {
		t.Errorf("resolved: total=%d rows=%v err=%v", total, resolved, err)
	}

	unresolved, total, err := ListCompanies(ctx, database, CompanyFilter{Resolution: ResolutionUnresolved}, 50, 0)
	if err != nil || total != 1 || unresolved[0].Slug != "tried" {
		t.Errorf("unresolved: total=%d rows=%v err=%v", total, unresolved, err)
	}

	unattempted, total, err := ListCompanies(ctx, database, CompanyFilter{Resolution: ResolutionUnattempted}, 50, 0)
	if err != nil || total != 1 || unattempted[0].Slug != "untouched" {
		t.Errorf("unattempted: total=%d rows=%v err=%v", total, unattempted, err)
	}

	// The attempt count is carried so the UI can show it without a second query.
	if unresolved[0].AttemptCount != 1 {
		t.Errorf("attempt count = %d, want 1", unresolved[0].AttemptCount)
	}
	if unattempted[0].AttemptCount != 0 {
		t.Errorf("attempt count = %d, want 0 for a company never looked at", unattempted[0].AttemptCount)
	}
}

func TestListCompaniesSearchMatchesNameAndSlug(t *testing.T) {
	database := newTestDB(t)
	seedDirectoryCompany(t, database, 1, "acme-corp", "Acme Corporation", "sp500")
	seedDirectoryCompany(t, database, 2, "beta", "Beta Inc.", "sp600")

	byName, total, err := ListCompanies(context.Background(), database, CompanyFilter{Search: "acme"}, 50, 0)
	if err != nil || total != 1 || byName[0].Slug != "acme-corp" {
		t.Errorf("by name: total=%d rows=%v err=%v", total, byName, err)
	}
	bySlug, total, err := ListCompanies(context.Background(), database, CompanyFilter{Search: "beta"}, 50, 0)
	if err != nil || total != 1 || bySlug[0].Slug != "beta" {
		t.Errorf("by slug: total=%d rows=%v err=%v", total, bySlug, err)
	}
}

// A user-supplied wildcard must be matched literally rather than widening the result set.
func TestListCompaniesSearchEscapesWildcards(t *testing.T) {
	database := newTestDB(t)
	seedDirectoryCompany(t, database, 1, "acme", "Acme Corporation", "sp500")
	seedDirectoryCompany(t, database, 2, "beta", "Beta Inc.", "sp600")

	// "%" as a literal matches nothing, whereas an unescaped LIKE would match everything.
	rows, total, err := ListCompanies(context.Background(), database, CompanyFilter{Search: "%"}, 50, 0)
	if err != nil {
		t.Fatalf("ListCompanies: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Errorf("a literal %% matched %d companies; the wildcard is not escaped", total)
	}
}

func TestListCompaniesPaginates(t *testing.T) {
	database := newTestDB(t)
	for i := int64(1); i <= 5; i++ {
		seedDirectoryCompany(t, database, i, "c-"+string(rune('a'+i)), "Company "+string(rune('A'+i)), "sp500")
	}

	page, total, err := ListCompanies(context.Background(), database, CompanyFilter{}, 2, 0)
	if err != nil {
		t.Fatalf("ListCompanies: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5 (the total is over the whole filter, not the page)", total)
	}
	if len(page) != 2 {
		t.Errorf("page size = %d, want 2", len(page))
	}

	second, _, err := ListCompanies(context.Background(), database, CompanyFilter{}, 2, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second[0].ID == page[0].ID {
		t.Error("the second page repeats the first")
	}
}

func TestListCompanyAttemptsReturnsWholeTrailNewestFirst(t *testing.T) {
	database := newTestDB(t)
	seedDirectoryCompany(t, database, 1, "acme", "Acme Corporation", "sp500")

	writer := NewResolutionWriter(database, "")
	ctx := context.Background()

	first, _, _ := StartRun(ctx, database, 23)
	if _, err := writer.Write(ctx, first, 0, Resolution{
		CompanyID: 1, CompanySlug: "acme",
		Attempts: []ResolutionAttempt{
			{Source: "homepage", CandidateURL: "https://acme.example.com/", Kind: "website",
				ValidationState: "rejected", Reason: "forbidden"},
		},
	}); err != nil {
		t.Fatalf("first run write: %v", err)
	}

	second, _, _ := StartRun(ctx, database, 23)
	if _, err := writer.Write(ctx, second, 0, Resolution{
		CompanyID: 1, CompanySlug: "acme",
		CareerSiteURL: "https://acme.example.com/careers", CareerSiteSource: CareerSiteSourceAnchorScan,
		Attempts: []ResolutionAttempt{
			{Source: "nav_anchor", CandidateURL: "https://acme.example.com/careers", Kind: "career_site",
				ValidationState: "accepted"},
		},
	}); err != nil {
		t.Fatalf("second run write: %v", err)
	}

	attempts, err := ListCompanyAttempts(ctx, database, 1)
	if err != nil {
		t.Fatalf("ListCompanyAttempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("got %d attempts, want 2 (the trail is kept across runs)", len(attempts))
	}
	if attempts[0].RunID.Int64 != second {
		t.Errorf("first attempt is from run %d, want the newest run %d", attempts[0].RunID.Int64, second)
	}
	// The newest run accepted, so its reason is NULL and its state is accepted.
	if attempts[0].ValidationState != "accepted" {
		t.Errorf("newest attempt state = %q, want accepted", attempts[0].ValidationState)
	}
	if attempts[0].RejectionReason.Valid {
		t.Errorf("newest attempt carries reason %q, want NULL for an accepted row", attempts[0].RejectionReason.String)
	}
	// The older run rejected, so its reason survives in the trail.
	if attempts[1].ValidationState != "rejected" {
		t.Errorf("older attempt state = %q, want rejected", attempts[1].ValidationState)
	}
	if attempts[1].RejectionReason.String != "forbidden" {
		t.Errorf("older attempt reason = %q, want forbidden", attempts[1].RejectionReason.String)
	}
}

func TestGetCompanyReturnsTheRow(t *testing.T) {
	database := newTestDB(t)
	seedDirectoryCompany(t, database, 7, "acme", "Acme Corporation", "sp500")

	got, err := GetCompany(context.Background(), database, 7)
	if err != nil {
		t.Fatalf("GetCompany: %v", err)
	}
	if got.Slug != "acme" || got.Name != "Acme Corporation" {
		t.Errorf("got %+v", got)
	}

	if _, err := GetCompany(context.Background(), database, 999); err == nil {
		t.Error("GetCompany found a company that does not exist")
	}
}
