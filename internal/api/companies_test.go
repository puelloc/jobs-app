// companies_test.go: black-box tests for the company-directory endpoints.
//
// Same discipline as handlers_test.go: the real router over a real migrated database, decoding into
// local types that mirror the documented contract rather than into the api package's own types, so a
// wrong json tag fails the test instead of being masked by symmetric encode/decode.
package api

import (
	"database/sql"
	"encoding/json"
	"testing"
)

// --- contract mirrors -----------------------------------------------------

type companyJSON struct {
	ID                int64   `json:"id"`
	Slug              string  `json:"slug"`
	Name              string  `json:"name"`
	Industry          *string `json:"industry"`
	SubIndustry       *string `json:"gics_sub_industry"`
	Headquarters      *string `json:"headquarters_location"`
	IndexMembership   *string `json:"index_membership"`
	Website           *string `json:"website"`
	WebsiteSource     *string `json:"website_source"`
	CareerSiteURL     *string `json:"career_site_url"`
	CareerSiteSource  *string `json:"career_site_source"`
	CareerSiteTitle   *string `json:"career_site_title"`
	CareerSiteVerdict *string `json:"career_site_url_verdict"`
	AttemptCount      int64   `json:"attempt_count"`
	UpdatedAt         string  `json:"updated_at"`
}

type companyListJSON struct {
	Companies []companyJSON `json:"companies"`
	Limit     int           `json:"limit"`
	Offset    int           `json:"offset"`
	Total     int64         `json:"total"`
}

type attemptJSON struct {
	ID              int64   `json:"id"`
	RunID           *int64  `json:"run_id"`
	AttemptIndex    int64   `json:"attempt_index"`
	Source          string  `json:"source"`
	CandidateURL    string  `json:"candidate_url"`
	Kind            string  `json:"candidate_kind"`
	HTTPStatus      *int64  `json:"http_status"`
	FinalURL        *string `json:"final_url"`
	Title           *string `json:"title"`
	ValidationState string  `json:"validation_status"`
	RejectionReason *string `json:"rejection_reason"`
	EvidencePath    *string `json:"evidence_path"`
	CreatedAt       string  `json:"created_at"`
}

type companyDetailJSON struct {
	Company  companyJSON   `json:"company"`
	Attempts []attemptJSON `json:"attempts"`
}

type churnEndpointJSON struct {
	RunID int64   `json:"run_id"`
	URL   string  `json:"url"`
	Title *string `json:"title"`
	At    string  `json:"at"`
}

type churnChangeJSON struct {
	CompanyID int64             `json:"company_id"`
	Slug      string            `json:"slug"`
	Name      string            `json:"name"`
	From      churnEndpointJSON `json:"from"`
	To        churnEndpointJSON `json:"to"`
}

type churnResponseJSON struct {
	Changes []churnChangeJSON `json:"changes"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
	Total   int64             `json:"total"`
}

// --- fixtures -------------------------------------------------------------

func decodeCompanies(t *testing.T, body []byte) companyListJSON {
	t.Helper()
	var got companyListJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode company list %q: %v", body, err)
	}
	return got
}

// --- tests ----------------------------------------------------------------

// A company with no resolution yet must marshall its nullable columns as null, never as "": the UI
// has to tell "not looked up" from "looked up and empty".
func TestCompaniesListNullsAreNullNotEmptyStrings(t *testing.T) {
	h, database := newTestServerAndDB(t)
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name, industry, index_membership)
		 VALUES (1, 'acme', 'Acme Corporation', 'Industrials', 'sp500')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := do(t, h, "GET", "/api/companies")
	requireJSON(t, rec)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	got := decodeCompanies(t, rec.Body.Bytes())
	if got.Total != 1 || len(got.Companies) != 1 {
		t.Fatalf("total=%d companies=%d, want 1 and 1", got.Total, len(got.Companies))
	}
	c := got.Companies[0]
	if c.Industry == nil || *c.Industry != "Industrials" {
		t.Errorf("industry = %v, want Industrials", c.Industry)
	}
	for name, ptr := range map[string]*string{
		"website": c.Website, "website_source": c.WebsiteSource,
		"career_site_url": c.CareerSiteURL, "career_site_source": c.CareerSiteSource,
		"career_site_title": c.CareerSiteTitle, "headquarters_location": c.Headquarters,
		"career_site_url_verdict": c.CareerSiteVerdict,
	} {
		if ptr != nil {
			t.Errorf("%s = %q, want null for a company with no resolution", name, *ptr)
		}
	}
	if c.AttemptCount != 0 {
		t.Errorf("attempt_count = %d, want 0", c.AttemptCount)
	}
}

// The verdict column reaches the wire: a validation pass's classification is exposed so the UI can
// badge confirmed/wrong/unverifiable without reading every attempt.
func TestCompaniesListExposesVerdict(t *testing.T) {
	h, database := newTestServerAndDB(t)
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name, career_site_url, career_site_url_verdict)
		 VALUES (1, 'acme', 'Acme Corporation', 'https://acme.example.com/careers', 'confirmed')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := do(t, h, "GET", "/api/companies")
	requireJSON(t, rec)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeCompanies(t, rec.Body.Bytes())
	if len(got.Companies) != 1 {
		t.Fatalf("companies = %d, want 1", len(got.Companies))
	}
	if c := got.Companies[0]; c.CareerSiteVerdict == nil || *c.CareerSiteVerdict != "confirmed" {
		t.Errorf("career_site_url_verdict = %v, want confirmed", c.CareerSiteVerdict)
	}
}

// An unknown filter value is a bad request, not a silent no-op: a typo must not read as a result.
func TestCompaniesEndpointRejectsUnknownFilters(t *testing.T) {
	h, _ := newTestServerAndDB(t)

	for _, target := range []string{
		"/api/companies?index=sp900",
		"/api/companies?resolution=resovled",
		"/api/companies?limit=0",
		"/api/companies?limit=101",
		"/api/companies?offset=-1",
	} {
		rec := do(t, h, "GET", target)
		requireJSON(t, rec)
		if rec.Code != 400 {
			t.Errorf("%s: status = %d, want 400 (body %s)", target, rec.Code, rec.Body.String())
		}
	}
}

func TestCompanyDetailReturns404ForAnUnknownID(t *testing.T) {
	h, _ := newTestServerAndDB(t)
	rec := do(t, h, "GET", "/api/companies/424242")
	requireJSON(t, rec)
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCompanyDetailRejectsANonIntegerID(t *testing.T) {
	h, _ := newTestServerAndDB(t)
	rec := do(t, h, "GET", "/api/companies/abc")
	requireJSON(t, rec)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// The detail endpoint exposes the whole attempt trail: a wrong final answer is only diagnosable
// beside the candidates that were refused before it.
func TestCompanyDetailExposesTheAttemptTrail(t *testing.T) {
	h, database := newTestServerAndDB(t)
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name) VALUES (1, 'acme', 'Acme Corporation')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO scrape_runs (id, platform_id, started_at, status) VALUES (9, 23, '2026-09-26T00:00:00.000Z', 'ok')`); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := database.Exec(`
INSERT INTO url_resolution_attempts
    (company_id, run_id, attempt_index, source, candidate_url, candidate_kind,
     http_status, final_url, title, validation_status, rejection_reason)
VALUES
    (1, 9, 0, 'homepage', 'https://acme.example.com/', 'website', 403, NULL, NULL, 'rejected', 'forbidden'),
    (1, 9, 1, 'nav_anchor', 'https://acme.example.com/careers', 'career_site',
     200, 'https://acme.example.com/careers', 'Careers at Acme', 'accepted', NULL)`); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}

	rec := do(t, h, "GET", "/api/companies/1")
	requireJSON(t, rec)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var got companyDetailJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode detail %q: %v", rec.Body.String(), err)
	}
	if got.Company.Slug != "acme" {
		t.Errorf("company slug = %q", got.Company.Slug)
	}
	if len(got.Attempts) != 2 {
		t.Fatalf("attempts = %d, want the whole trail of 2", len(got.Attempts))
	}

	rejected := got.Attempts[0]
	if rejected.ValidationState != "rejected" {
		t.Errorf("first attempt state = %q, want rejected", rejected.ValidationState)
	}
	if rejected.RejectionReason == nil || *rejected.RejectionReason != "forbidden" {
		t.Errorf("first attempt reason = %v, want forbidden", rejected.RejectionReason)
	}
	if rejected.FinalURL != nil {
		t.Errorf("first attempt final_url = %q, want null: a 403 has no document", *rejected.FinalURL)
	}

	accepted := got.Attempts[1]
	if accepted.ValidationState != "accepted" {
		t.Errorf("second attempt state = %q", accepted.ValidationState)
	}
	// An accepted attempt has a null reason, which the schema enforces and the wire shape preserves.
	if accepted.RejectionReason != nil {
		t.Errorf("accepted attempt reason = %q, want null", *accepted.RejectionReason)
	}
	if accepted.Title == nil || *accepted.Title != "Careers at Acme" {
		t.Errorf("accepted attempt title = %v", accepted.Title)
	}
	if accepted.RunID == nil || *accepted.RunID != 9 {
		t.Errorf("run_id = %v, want 9", accepted.RunID)
	}
}

// The job endpoints must keep working: the company routes were added to the same mux.
func TestJobRoutesStillWorkAlongsideCompanyRoutes(t *testing.T) {
	h, _ := newTestServerAndDB(t)
	for _, target := range []string{"/api/jobs", "/api/companies"} {
		rec := do(t, h, "GET", target)
		requireJSON(t, rec)
		if rec.Code != 200 {
			t.Errorf("%s: status = %d, want 200", target, rec.Code)
		}
	}
	// A wrong method on a company route is still a 405 in the JSON envelope.
	rec := do(t, h, "POST", "/api/companies")
	requireJSON(t, rec)
	if rec.Code != 405 {
		t.Errorf("POST /api/companies status = %d, want 405", rec.Code)
	}
}

// seedChurnFixture writes one company resolved to two different careers URLs in two runs.
func seedChurnFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO companies (id, slug, name, index_membership)
		VALUES (1, 'acme', 'Acme Corporation', 'sp500')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	for _, id := range []int{4, 5} {
		if _, err := database.Exec(`INSERT INTO scrape_runs (id, platform_id, started_at, status)
			VALUES (?, 23, '2026-09-26T00:00:00.000Z', 'ok')`, id); err != nil {
			t.Fatalf("seed run %d: %v", id, err)
		}
	}
	if _, err := database.Exec(`
INSERT INTO url_resolution_attempts
    (company_id, run_id, attempt_index, source, candidate_url, candidate_kind,
     http_status, final_url, title, validation_status, rejection_reason)
VALUES
    (1, 4, 0, 'nav_anchor', 'https://acme.example.com/jobs', 'career_site',
     200, 'https://acme.example.com/jobs', 'Jobs at Acme', 'accepted', NULL),
    (1, 5, 0, 'nav_anchor', 'https://acme.example.com/careers', 'career_site',
     200, 'https://acme.example.com/careers', 'Careers at Acme', 'accepted', NULL)`); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}
}

// The churn endpoint is the only surface that shows a careers URL moving between runs.
func TestChurnEndpointReportsAChangedURL(t *testing.T) {
	h, database := newTestServerAndDB(t)
	seedChurnFixture(t, database)

	rec := do(t, h, "GET", "/api/companies/churn")
	requireJSON(t, rec)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var got churnResponseJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode churn %q: %v", rec.Body.String(), err)
	}
	if got.Total != 1 || len(got.Changes) != 1 {
		t.Fatalf("total=%d changes=%d, want 1 and 1", got.Total, len(got.Changes))
	}
	c := got.Changes[0]
	if c.CompanyID != 1 || c.Slug != "acme" || c.Name != "Acme Corporation" {
		t.Errorf("company = %d/%s/%s", c.CompanyID, c.Slug, c.Name)
	}
	if c.From.RunID != 4 || c.From.URL != "https://acme.example.com/jobs" {
		t.Errorf("from = %+v", c.From)
	}
	if c.To.RunID != 5 || c.To.URL != "https://acme.example.com/careers" {
		t.Errorf("to = %+v", c.To)
	}
	if c.From.Title == nil || *c.From.Title != "Jobs at Acme" {
		t.Errorf("from title = %v", c.From.Title)
	}
	if c.From.At == "" || c.To.At == "" {
		t.Errorf("timestamps missing: %+v", c)
	}
}

// No changes must be an empty array, not null: the UI iterates the field and a null would be a
// contract bug rather than an empty state.
func TestChurnEndpointReturnsEmptyArrayWhenNothingChanged(t *testing.T) {
	h, _ := newTestServerAndDB(t)
	rec := do(t, h, "GET", "/api/companies/churn")
	requireJSON(t, rec)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var got churnResponseJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode churn %q: %v", rec.Body.String(), err)
	}
	if got.Changes == nil {
		t.Error("changes was JSON null; want []")
	}
	if got.Total != 0 {
		t.Errorf("total = %d, want 0", got.Total)
	}
}

// The churn route shares the companies prefix, so its literal segment must win over the {id}
// wildcard, and an unknown filter on it must still be a 400.
func TestChurnRouteIsNotParsedAsACompanyIDAndValidatesFilters(t *testing.T) {
	h, _ := newTestServerAndDB(t)

	// "churn" is not an integer; if the {id} route had won this would be a 400.
	rec := do(t, h, "GET", "/api/companies/churn")
	requireJSON(t, rec)
	if rec.Code != 200 {
		t.Errorf("/api/companies/churn status = %d, want 200", rec.Code)
	}

	for _, target := range []string{
		"/api/companies/churn?index=sp900",
		"/api/companies/churn?limit=0",
		"/api/companies/churn?limit=101",
		"/api/companies/churn?offset=-1",
	} {
		rec := do(t, h, "GET", target)
		requireJSON(t, rec)
		if rec.Code != 400 {
			t.Errorf("%s: status = %d, want 400 (body %s)", target, rec.Code, rec.Body.String())
		}
	}
}
