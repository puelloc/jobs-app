// runresolve_test.go: tests for the resolution run.
//
// Every response is served from a map through the same Fetcher seam the tiers use, so the whole run -
// tier 1, the ladder, the ATS tier and persistence - executes without a socket.
//
// The fakes here are hand-written rather than a shared stub, and each was checked against the real
// contract on the three points that have bitten before: the routing predicate, the URL construction,
// and what happens on a miss. A fake that returns a body for every URL would hide a router bug.
package runresolve

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jobsapp/internal/careers"
	"jobsapp/internal/db"
	"jobsapp/internal/store"
)

// pageFetcher routes on the API action and the requested host, and returns 404 for anything it does
// not know - so a router mistake shows up as a failed resolution rather than a passing test.
type pageFetcher struct {
	// pages maps a URL to a canned response.
	pages map[string]careers.Response
	// calls records every URL, so a test can assert on the bound and the count.
	calls    []string
	bounds   []int64
	fallback careers.Response
}

func (f *pageFetcher) Fetch(_ context.Context, rawURL string, maxBodyBytes int64) (careers.Response, error) {
	f.calls = append(f.calls, rawURL)
	f.bounds = append(f.bounds, maxBodyBytes)

	if r, ok := f.pages[rawURL]; ok {
		if r.FinalURL == "" {
			r.FinalURL = rawURL
		}
		if maxBodyBytes > 0 && int64(len(r.Body)) > maxBodyBytes {
			r.Body = r.Body[:maxBodyBytes]
			r.Truncated = true
		}
		return r, nil
	}
	// A miss is a 404, not a zero value: an empty response would be recorded as an empty body and
	// look like a server problem rather than a fixture gap.
	if f.fallback.Status != 0 {
		return f.fallback, nil
	}
	return careers.Response{FinalURL: rawURL, Status: 404, ContentType: "text/html", Body: []byte("<html>not found</html>")}, nil
}

func htmlResponse(url, title, body string) careers.Response {
	return careers.Response{FinalURL: url, Status: 200, ContentType: "text/html; charset=utf-8",
		Body: []byte("<html><head><title>" + title + "</title></head><body>" + body + "</body></html>")}
}

func jsonResponse(url, body string) careers.Response {
	return careers.Response{FinalURL: url, Status: 200, ContentType: "application/json", Body: []byte(body)}
}

// newRunTestDB opens a migrated database and seeds one company.
func newRunTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, dir
}

func seedCompany(t *testing.T, database *sql.DB, id int64, slug, name string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name) VALUES (?, ?, ?)`, id, slug, name); err != nil {
		t.Fatalf("seed %s: %v", slug, err)
	}
}

// tier1Stub answers the two tier-1 APIs for a title. A test that enables ResolveHomepages needs it,
// or the run's homepage lookup goes to the network.
func tier1Stub(t *testing.T, title, website string) *pageFetcher {
	t.Helper()
	return &pageFetcher{pages: map[string]careers.Response{}}
}

// acmeRun builds the fixture set for one resolvable company: a homepage whose navigation links to a
// careers page, and that careers page.
func acmeRun() map[string]careers.Response {
	const home = "https://www.acme.com/"
	const careersURL = "https://www.acme.com/careers"
	return map[string]careers.Response{
		home: htmlResponse(home, "Acme Corporation",
			`<nav><a href="/about">About</a><a href="/careers">Careers</a></nav>`),
		"https://www.acme.com/robots.txt":  {FinalURL: "https://www.acme.com/robots.txt", Status: 404, ContentType: "text/plain"},
		"https://www.acme.com/sitemap.xml": {FinalURL: "https://www.acme.com/sitemap.xml", Status: 404, ContentType: "application/xml"},
		careersURL:                         htmlResponse(careersURL, "Careers at Acme", `<h1>Careers</h1><p>Search jobs</p>`),
	}
}

func acmeCompany(website string) Company {
	return Company{ID: 1, Slug: "acme", Name: "Acme Corporation", Article: "Acme Corporation", Website: website}
}

func newRunner(t *testing.T, database *sql.DB, fetcher careers.Fetcher) Runner {
	t.Helper()
	return Runner{DB: database, Fetcher: fetcher}
}

// --- exit-code behaviour --------------------------------------------------

func TestResolutionRunExitsZeroOnFirstAccept(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	f := &pageFetcher{pages: acmeRun()}
	sum, err := newRunner(t, database, f).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", sum.Resolved)
	}
	if sum.RunID == 0 {
		t.Error("no run row was written")
	}

	var url, source string
	if err := database.QueryRow(
		`SELECT career_site_url, career_site_url_source FROM companies WHERE id = 1`).Scan(&url, &source); err != nil {
		t.Fatalf("read company: %v", err)
	}
	if url != "https://www.acme.com/careers" {
		t.Errorf("career_site_url = %q", url)
	}
	if source != string(store.CareerSiteSourceAnchorScan) {
		t.Errorf("career_site_url_source = %q, want %q", source, store.CareerSiteSourceAnchorScan)
	}
}

// A run that considers companies and resolves none is not an error, but it is not a success either.
func TestResolutionRunReportsZeroAccepts(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	// A homepage that is reachable but links nowhere useful.
	f := &pageFetcher{pages: map[string]careers.Response{
		"https://www.acme.com/": htmlResponse("https://www.acme.com/", "Acme", `<nav><a href="/about">About</a></nav>`),
	}}
	sum, err := newRunner(t, database, f).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Resolved != 0 {
		t.Errorf("Resolved = %d, want 0", sum.Resolved)
	}
	if sum.Found != 1 {
		t.Errorf("Found = %d, want 1", sum.Found)
	}

	// The run row exists and says what happened rather than claiming a clean success.
	var status, errText sql.NullString
	if err := database.QueryRow(
		`SELECT status, error_text FROM scrape_runs WHERE id = ?`, sum.RunID).Scan(&status, &errText); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status.String == "running" {
		t.Error("the run row was left running")
	}
	if !errText.Valid || errText.String == "" {
		t.Error("error_text is empty for a run that resolved nothing")
	}
}

// --- dry run --------------------------------------------------------------

func TestDryRunWritesAttemptsWithDryRunRun(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	sum, err := newRunner(t, database, &pageFetcher{pages: acmeRun()}).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir, DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var dry int
	if err := database.QueryRow(`SELECT dry_run FROM scrape_runs WHERE id = ?`, sum.RunID).Scan(&dry); err != nil {
		t.Fatalf("read dry_run: %v", err)
	}
	if dry != 1 {
		t.Error("the run row is not marked dry_run")
	}

	var attempts int
	if err := database.QueryRow(
		`SELECT count(*) FROM url_resolution_attempts WHERE run_id = ?`, sum.RunID).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts == 0 {
		t.Error("a dry run recorded no attempts, which removes the reason to run it")
	}
}

// The wiring's own version of the non-destructiveness claim: not just the writer's, but through a
// full run with the real tiers.
func TestDryRunLeavesCompaniesUnchanged(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	snapshot := func() map[string]any {
		rows, err := database.Query(`SELECT * FROM companies WHERE id = 1`)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		if !rows.Next() {
			t.Fatal("no company row")
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		_ = rows.Scan(ptrs...)
		out := map[string]any{}
		for i, c := range cols {
			out[c] = vals[i]
		}
		return out
	}

	before := snapshot()
	if _, err := newRunner(t, database, &pageFetcher{pages: acmeRun()}).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir, DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	after := snapshot()

	for col, bv := range before {
		if av, ok := after[col]; ok && av != bv {
			t.Errorf("dry run changed companies.%s: %v -> %v", col, bv, av)
		}
	}
}

func TestDryRunWritesNoCompanyApplicationPlatforms(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	// A company whose careers page is an Ashby board, so the ATS path is exercised.
	pages := map[string]careers.Response{
		"https://www.acme.com/": htmlResponse("https://www.acme.com/", "Acme Corporation",
			`<nav><a href="https://jobs.ashbyhq.com/acme">Careers</a></nav>`),
		"https://jobs.ashbyhq.com/acme": htmlResponse("https://jobs.ashbyhq.com/acme", "Acme Jobs",
			`<h1>Acme Careers</h1><p>Search jobs</p>`),
		"https://api.ashbyhq.com/posting-api/job-board/acme": jsonResponse(
			"https://api.ashbyhq.com/posting-api/job-board/acme", `{"jobs":[{"id":"1"}]}`),
	}

	if _, err := newRunner(t, database, &pageFetcher{pages: pages}).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir, DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var platforms int
	if err := database.QueryRow(`SELECT count(*) FROM company_application_platforms`).Scan(&platforms); err != nil {
		t.Fatalf("count: %v", err)
	}
	if platforms != 0 {
		t.Errorf("a dry run wrote %d company_application_platforms rows, want 0", platforms)
	}
}

// The same run without the flag does write the platform, which is what makes the dry run a
// meaningful prediction rather than a different code path.
func TestRealRunWritesCompanyApplicationPlatforms(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	pages := map[string]careers.Response{
		"https://www.acme.com/": htmlResponse("https://www.acme.com/", "Acme Corporation",
			`<nav><a href="https://jobs.ashbyhq.com/acme">Careers</a></nav>`),
		"https://jobs.ashbyhq.com/acme": htmlResponse("https://jobs.ashbyhq.com/acme", "Acme Jobs",
			`<h1>Acme Careers</h1><p>Search jobs</p>`),
		"https://api.ashbyhq.com/posting-api/job-board/acme": jsonResponse(
			"https://api.ashbyhq.com/posting-api/job-board/acme", `{"jobs":[{"id":"1"}]}`),
	}

	if _, err := newRunner(t, database, &pageFetcher{pages: pages}).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var platformID int64
	var baseURL string
	if err := database.QueryRow(
		`SELECT platform_id, base_url FROM company_application_platforms WHERE company_id = 1`).
		Scan(&platformID, &baseURL); err != nil {
		t.Fatalf("read platform: %v", err)
	}
	if platformID != 14 {
		t.Errorf("platform_id = %d, want 14 (ashby)", platformID)
	}
	if baseURL != "https://jobs.ashbyhq.com/acme" {
		t.Errorf("base_url = %q, want the concrete tenant URL", baseURL)
	}

	// And the stored career site is the tenant URL, not the API endpoint.
	var stored string
	database.QueryRow(`SELECT career_site_url FROM companies WHERE id = 1`).Scan(&stored)
	if stored != "https://jobs.ashbyhq.com/acme" {
		t.Errorf("career_site_url = %q, want the tenant URL", stored)
	}
}

// --- flag semantics -------------------------------------------------------

func TestOnlySlugsRestrictsCompanySet(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")
	seedCompany(t, database, 2, "other", "Other Corporation")

	f := &pageFetcher{pages: acmeRun()}
	sum, err := newRunner(t, database, f).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/"), {ID: 2, Slug: "other", Name: "Other"}},
		Options{DataDir: dir, OnlySlugs: []string{"acme"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Found != 1 {
		t.Errorf("Found = %d, want 1", sum.Found)
	}
	if sum.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", sum.Skipped)
	}
	for _, url := range f.calls {
		if strings.Contains(url, "other") {
			t.Errorf("fetched %q for an excluded company", url)
		}
	}
}

func TestRefreshSkipsResolvedCompanies(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	settled := acmeCompany("https://www.acme.com/")
	settled.CareerSiteURL = "https://www.acme.com/careers"

	f := &pageFetcher{pages: acmeRun()}
	sum, err := newRunner(t, database, f).Run(context.Background(), []Company{settled}, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Skipped != 1 || sum.Found != 0 {
		t.Errorf("Skipped=%d Found=%d, want 1 and 0", sum.Skipped, sum.Found)
	}
	if len(f.calls) != 0 {
		t.Errorf("fetched %v for a settled company", f.calls)
	}
}

func TestRefreshReResolvesResolvedCompanies(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	settled := acmeCompany("https://www.acme.com/")
	settled.CareerSiteURL = "https://www.acme.com/careers"

	f := &pageFetcher{pages: acmeRun()}
	sum, err := newRunner(t, database, f).Run(context.Background(), []Company{settled},
		Options{DataDir: dir, Refresh: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Found != 1 {
		t.Errorf("Found = %d, want 1 with --refresh", sum.Found)
	}
	if len(f.calls) == 0 {
		t.Error("--refresh made no requests")
	}
}

func TestRefreshFailedReResolvesNullsOnly(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")
	seedCompany(t, database, 2, "other", "Other Corporation")

	failed := acmeCompany("https://www.acme.com/")
	settled := Company{ID: 2, Slug: "other", Name: "Other", Website: "https://www.other.com/"}
	settled.CareerSiteURL = "https://www.other.com/careers"

	pages := acmeRun()
	f := &pageFetcher{pages: pages}
	sum, err := newRunner(t, database, f).Run(context.Background(), []Company{failed, settled},
		Options{DataDir: dir, RefreshFailed: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Found != 1 {
		t.Errorf("Found = %d, want 1 (only the unresolved company)", sum.Found)
	}
	if sum.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", sum.Skipped)
	}
}

func TestLimitCapsTheCompanySet(t *testing.T) {
	database, dir := newRunTestDB(t)
	for i := int64(1); i <= 5; i++ {
		seedCompany(t, database, i, fmt.Sprintf("company-%d", i), fmt.Sprintf("Company %d", i))
	}
	var companies []Company
	for i := int64(1); i <= 5; i++ {
		companies = append(companies, Company{ID: i, Slug: fmt.Sprintf("company-%d", i),
			Name: fmt.Sprintf("Company %d", i), Website: "https://www.acme.com/"})
	}

	sum, err := newRunner(t, database, &pageFetcher{pages: acmeRun()}).Run(context.Background(), companies,
		Options{DataDir: dir, Limit: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Found != 2 {
		t.Errorf("Found = %d, want 2", sum.Found)
	}
}

// A company with no website cannot be resolved by this run. That is the 10-K residue, and it must be
// counted as unresolved rather than skipped, because it was considered.
func TestCompanyWithoutAWebsiteIsCountedUnresolved(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	f := &pageFetcher{pages: acmeRun()}
	sum, err := newRunner(t, database, f).Run(context.Background(),
		[]Company{acmeCompany("")}, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Found != 1 || sum.Unresolved != 1 {
		t.Errorf("Found=%d Unresolved=%d, want 1 and 1", sum.Found, sum.Unresolved)
	}
	// With tier 1 disabled there is nothing to fetch at all: no homepage to scan and no API to ask.
	if len(f.calls) != 0 {
		t.Errorf("fetched %v with no homepage to scan and tier 1 off", f.calls)
	}
}

// --- counters and evidence ------------------------------------------------

func TestCountersDistinguishInsertFromUpdate(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	// First run inserts.
	first, err := newRunner(t, database, &pageFetcher{pages: acmeRun()}).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.Inserted != 1 || first.Updated != 0 {
		t.Errorf("first run inserted=%d updated=%d, want 1 and 0", first.Inserted, first.Updated)
	}

	// A second run with --refresh finds the same URL, so neither counter moves: re-confirming a
	// value is not a change.
	pages := acmeRun()
	second, err := newRunner(t, database, &pageFetcher{pages: pages}).Run(context.Background(),
		[]Company{Company{ID: 1, Slug: "acme", Name: "Acme Corporation", Website: "https://www.acme.com/",
			CareerSiteURL: "https://www.acme.com/careers"}},
		Options{DataDir: dir, Refresh: true})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.Inserted != 0 || second.Updated != 0 {
		t.Errorf("re-confirming the same URL moved the counters: inserted=%d updated=%d, want 0 and 0",
			second.Inserted, second.Updated)
	}
}

// Evidence bodies are retained, so a wrong pick can be examined after the fact.
func TestEvidenceBodiesAreRetained(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	if _, err := newRunner(t, database, &pageFetcher{pages: acmeRun()}).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rows, err := database.Query(`SELECT evidence_path FROM url_resolution_attempts WHERE evidence_path IS NOT NULL`)
	if err != nil {
		t.Fatalf("query evidence: %v", err)
	}
	defer rows.Close()

	var checked int
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("evidence %q is recorded but missing: %v", path, err)
		}
		if !strings.Contains(path, "careers") || !strings.Contains(path, "acme") {
			t.Errorf("evidence path = %q, want it under the company directory", path)
		}
		checked++
	}
	if checked == 0 {
		t.Error("no attempt recorded an evidence path")
	}
}

// Every fetch goes through the seam with a positive bound, so the limiter wraps the tier too.
func TestEveryFetchCarriesABound(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	f := &pageFetcher{pages: acmeRun()}
	if _, err := newRunner(t, database, f).Run(context.Background(),
		[]Company{acmeCompany("https://www.acme.com/")}, Options{DataDir: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.bounds) != len(f.calls) {
		t.Fatalf("bounds=%d calls=%d, want one bound per call", len(f.bounds), len(f.calls))
	}
	for i, b := range f.bounds {
		if b <= 0 {
			t.Errorf("call %s passed bound %d, want a positive limit", f.calls[i], b)
		}
	}
}

// --- guards ---------------------------------------------------------------

func TestRunRequiresDBAndFetcher(t *testing.T) {
	if _, err := (Runner{}).Run(context.Background(), nil, Options{}); err == nil {
		t.Error("Run ran with no database")
	}
	database, _ := newRunTestDB(t)
	if _, err := (Runner{DB: database}).Run(context.Background(), nil, Options{}); err == nil {
		t.Error("Run ran with no fetcher")
	}
}

// Nothing to do writes no run row, so a no-op cannot be mistaken for a pass that did work.
func TestNoWorkWritesNoRunRow(t *testing.T) {
	database, dir := newRunTestDB(t)
	seedCompany(t, database, 1, "acme", "Acme Corporation")

	sum, err := newRunner(t, database, &pageFetcher{}).Run(context.Background(), nil, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.RunID != 0 {
		t.Errorf("RunID = %d, want 0 when there is nothing to do", sum.RunID)
	}
	var runs int
	database.QueryRow(`SELECT count(*) FROM scrape_runs`).Scan(&runs)
	if runs != 0 {
		t.Errorf("scrape_runs rows = %d, want 0", runs)
	}
}
