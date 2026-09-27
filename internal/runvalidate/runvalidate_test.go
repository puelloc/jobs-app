package runvalidate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jobsapp/internal/browseruse"
	"jobsapp/internal/careers"
	"jobsapp/internal/db"
)

// The tests here run against a real migrated SQLite file rather than a stub store, because the
// things worth pinning are the ones the schema enforces: that a validation run moves the
// last-verified stamp without touching the stored URL, that an accepted attempt carries no
// rejection reason, and that the run is recorded against its own platform.

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func seedCompany(t *testing.T, database *sql.DB, slug, name, url string) int64 {
	t.Helper()
	var id int64
	err := database.QueryRow(`
INSERT INTO companies (slug, name, career_site_url, career_site_url_source)
VALUES (?, ?, ?, 'nav_anchor')
RETURNING id`, slug, name, url).Scan(&id)
	if err != nil {
		t.Fatalf("seed company %s: %v", slug, err)
	}
	return id
}

// fakeRenderer answers per URL. It records every request so a test can assert which tiers ran.
type fakeRenderer struct {
	mu    sync.Mutex
	byURL map[string]browseruse.Result
	errs  map[string]error
	calls []browseruse.Request
}

func newFakeRenderer() *fakeRenderer {
	return &fakeRenderer{byURL: map[string]browseruse.Result{}, errs: map[string]error{}}
}

func (f *fakeRenderer) set(url string, res browseruse.Result) *fakeRenderer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byURL[url] = res
	return f
}

func (f *fakeRenderer) fail(url string, err error) *fakeRenderer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[url] = err
	return f
}

func (f *fakeRenderer) Render(_ context.Context, req browseruse.Request) (browseruse.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	res, ok := f.byURL[req.URL]
	err := f.errs[req.URL]
	f.mu.Unlock()
	if err != nil {
		return browseruse.Result{}, err
	}
	if !ok {
		return browseruse.Result{}, fmt.Errorf("fakeRenderer: no result for %s", req.URL)
	}
	return res, nil
}

func (f *fakeRenderer) sources() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, string(c.Mode)+":"+c.URL)
	}
	return out
}

func (f *fakeRenderer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func careersPage(title string, status int) browseruse.Result {
	return browseruse.Result{
		OK:          true,
		Mode:        string(browseruse.ModeRender),
		FinalURL:    "https://example.com/careers",
		Status:      status,
		ContentType: "text/html; charset=utf-8",
		Title:       title,
		Body:        []byte("<html><title>" + title + "</title><h1>Careers</h1><p>Open positions</p></html>"),
	}
}

func runValidation(t *testing.T, database *sql.DB, renderer, agent browseruse.Renderer, opts Options) Summary {
	t.Helper()
	companies, err := LoadStoredCompanies(context.Background(), database, StoredFilter{OnlySlugs: opts.OnlySlugs})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	runner := Runner{DB: database, Renderer: renderer, Agent: agent}
	sum, err := runner.Run(context.Background(), companies, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return sum
}

func TestValidateConfirmsAStoredURLTheGateAccepts(t *testing.T) {
	database := newTestDB(t)
	id := seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
	renderer := newFakeRenderer().set("https://example.com/careers", browseruse.Result{
		OK: true, Mode: "render", FinalURL: "https://example.com/careers", Status: 200,
		ContentType: "text/html; charset=utf-8", Title: "Careers | Example Corp",
		Body: []byte("<html><title>Careers | Example Corp</title><h1>Careers</h1></html>"),
	})

	sum := runValidation(t, database, renderer, nil, Options{})

	if sum.Found != 1 || sum.Confirmed != 1 || sum.Wrong != 0 || sum.Unverifiable != 0 || sum.Failed != 0 {
		t.Fatalf("summary = %+v, want one confirmed company", sum)
	}

	var status, reason sql.NullString
	if err := database.QueryRow(`
SELECT validation_status, rejection_reason FROM url_resolution_attempts WHERE company_id = ?`, id).
		Scan(&status, &reason); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if status.String != "accepted" || reason.Valid {
		t.Errorf("attempt = %q/%v, want accepted with a NULL reason", status.String, reason)
	}

	// The stamp moves; the URL does not.
	var url, source string
	var checkedAt sql.NullString
	var httpStatus sql.NullInt64
	if err := database.QueryRow(`
SELECT career_site_url, career_site_url_source, career_site_url_checked_at, career_site_url_http_status
  FROM companies WHERE id = ?`, id).Scan(&url, &source, &checkedAt, &httpStatus); err != nil {
		t.Fatalf("read company: %v", err)
	}
	if url != "https://example.com/careers" {
		t.Errorf("career_site_url = %q, want the stored value untouched", url)
	}
	if source != "nav_anchor" {
		t.Errorf("career_site_url_source = %q, want the original source untouched", source)
	}
	if !checkedAt.Valid || checkedAt.String == "" {
		t.Error("career_site_url_checked_at was not stamped")
	}
	if !httpStatus.Valid || httpStatus.Int64 != 200 {
		t.Errorf("career_site_url_http_status = %v, want 200", httpStatus)
	}

	// The verdict is current state on the row, in the same transaction as the attempt, so the two
	// can never disagree about what we currently believe. It carries the URL it judged, which is
	// what makes the skip rule safe across a resolver changing that URL.
	var verdict, verdictAt, verdictURL sql.NullString
	var verdictRun sql.NullInt64
	if err := database.QueryRow(`
SELECT career_site_url_verdict, career_site_url_verdict_at, career_site_url_verdict_run_id,
       career_site_url_verdict_url
  FROM companies WHERE id = ?`, id).Scan(&verdict, &verdictAt, &verdictRun, &verdictURL); err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	if verdict.String != string(CategoryConfirmed) {
		t.Errorf("verdict = %q, want %q", verdict.String, CategoryConfirmed)
	}
	if verdictURL.String != "https://example.com/careers" {
		t.Errorf("verdict url = %q, want the URL that was measured", verdictURL.String)
	}
	if !verdictAt.Valid || verdictAt.String == "" {
		t.Error("career_site_url_verdict_at was not stamped")
	}
	if !verdictRun.Valid || verdictRun.Int64 != sum.RunID {
		t.Errorf("verdict run id = %v, want %d", verdictRun, sum.RunID)
	}
}

func TestValidateMarksAWrongPageWithoutClearingTheStoredURL(t *testing.T) {
	database := newTestDB(t)
	id := seedCompany(t, database, "widget-co", "Widget Co", "https://example.com/products")
	renderer := newFakeRenderer().set("https://example.com/products", browseruse.Result{
		OK: true, Mode: "render", FinalURL: "https://example.com/products", Status: 200,
		ContentType: "text/html", Title: "Our Products | Widget Co",
		Body: []byte("<html><title>Our Products | Widget Co</title></html>"),
	})

	sum := runValidation(t, database, renderer, nil, Options{})

	if sum.Wrong != 1 || sum.Confirmed != 0 {
		t.Fatalf("summary = %+v, want one wrong", sum)
	}

	var reason string
	if err := database.QueryRow(`
SELECT rejection_reason FROM url_resolution_attempts WHERE company_id = ?`, id).Scan(&reason); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if reason != string(careers.OutcomeProductOrInvestorPath) {
		t.Errorf("reason = %q, want %q", reason, careers.OutcomeProductOrInvestorPath)
	}

	var url string
	if err := database.QueryRow(`SELECT career_site_url FROM companies WHERE id = ?`, id).Scan(&url); err != nil {
		t.Fatalf("read company: %v", err)
	}
	if url != "https://example.com/products" {
		t.Errorf("career_site_url = %q; a validation run must never rewrite the value it measures", url)
	}
}

// A blocked site proves nothing about the stored URL. Recording it as "wrong" is the single most
// expensive mistake this package could make, because it turns a measurement into false accusations.
func TestValidateRecordsBlockedAndDeadSitesAsUnverifiable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result browseruse.Result
	}{
		{name: "403", result: browseruse.Result{OK: true, Mode: "render", FinalURL: "https://example.com/careers", Status: 403, ContentType: "text/html", Body: []byte("denied")}},
		{name: "bot wall", result: browseruse.Result{OK: true, Mode: "render", FinalURL: "https://example.com/careers", Status: 200, ContentType: "text/html", Title: "Just a moment...", Body: []byte("cf_chl_opt")}},
		{name: "navigation failure", result: browseruse.Result{OK: false, Mode: "render", ErrorKind: browseruse.KindNavigation, Error: "net::ERR_NAME_NOT_RESOLVED"}},
		{name: "timeout", result: browseruse.Result{OK: false, Mode: "render", ErrorKind: browseruse.KindTimeout, Error: "budget expired"}},
		{name: "server error", result: browseruse.Result{OK: true, Mode: "render", FinalURL: "https://example.com/careers", Status: 503, ContentType: "text/html", Body: []byte("down")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := newTestDB(t)
			seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
			renderer := newFakeRenderer().set("https://example.com/careers", tc.result)

			sum := runValidation(t, database, renderer, nil, Options{})

			if sum.Unverifiable != 1 || sum.Wrong != 0 {
				t.Fatalf("summary = %+v, want one unverifiable and none wrong", sum)
			}
		})
	}
}

func TestValidateRecordsItsOwnPlatformAndStopsTheCompanyStampAtDryRun(t *testing.T) {
	database := newTestDB(t)
	id := seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
	renderer := newFakeRenderer().set("https://example.com/careers", careersPage("Careers | Example Corp", 200))

	sum := runValidation(t, database, renderer, nil, Options{DryRun: true})

	var platformID int64
	var dryRun int
	var found, inserted int64
	if err := database.QueryRow(`
SELECT platform_id, dry_run, items_found, items_inserted FROM scrape_runs WHERE id = ?`, sum.RunID).
		Scan(&platformID, &dryRun, &found, &inserted); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if platformID != PlatformID {
		t.Errorf("platform_id = %d, want %d", platformID, PlatformID)
	}
	if dryRun != 1 {
		t.Errorf("dry_run = %d, want 1", dryRun)
	}
	if found != 1 || inserted != 1 {
		t.Errorf("counters = found %d inserted %d, want 1/1", found, inserted)
	}

	var checkedAt sql.NullString
	var attempts int
	if err := database.QueryRow(`SELECT career_site_url_checked_at FROM companies WHERE id = ?`, id).Scan(&checkedAt); err != nil {
		t.Fatalf("read company: %v", err)
	}
	if checkedAt.Valid {
		t.Errorf("checked_at = %v; a dry run must move no stamp", checkedAt.String)
	}
	var verdict sql.NullString
	if err := database.QueryRow(`SELECT career_site_url_verdict FROM companies WHERE id = ?`, id).Scan(&verdict); err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	if verdict.Valid {
		t.Errorf("verdict = %q; a dry run must record no verdict", verdict.String)
	}
	if err := database.QueryRow(`SELECT count(*) FROM url_resolution_attempts WHERE run_id = ?`, sum.RunID).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1: a dry run's attempts are the whole point of running it", attempts)
	}
}

// The run row has to state the whole split, not just the good news: a run reporting "confirmed 480"
// while sixty-one proven-wrong pages sit unmentioned is the summary that misleads.
func TestValidateRecordsTheVerdictAndTheRunSplit(t *testing.T) {
	database := newTestDB(t)
	confirmed := seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
	wrong := seedCompany(t, database, "widget-co", "Widget Co", "https://example.com/products")
	blocked := seedCompany(t, database, "blocked-co", "Blocked Co", "https://example.com/blocked")

	renderer := newFakeRenderer().
		set("https://example.com/careers", careersPage("Careers | Example Corp", 200)).
		set("https://example.com/products", browseruse.Result{
			OK: true, Mode: "render", FinalURL: "https://example.com/products", Status: 200,
			ContentType: "text/html", Title: "Products | Widget Co",
			Body: []byte("<html><title>Products | Widget Co</title></html>"),
		}).
		set("https://example.com/blocked", browseruse.Result{
			OK: true, Mode: "render", FinalURL: "https://example.com/blocked", Status: 403,
			ContentType: "text/html", Body: []byte("denied"),
		})

	sum := runValidation(t, database, renderer, nil, Options{})
	if sum.Confirmed != 1 || sum.Wrong != 1 || sum.Unverifiable != 1 {
		t.Fatalf("summary = %+v, want one of each", sum)
	}

	for _, tc := range []struct {
		id   int64
		want Category
	}{
		{confirmed, CategoryConfirmed},
		{wrong, CategoryWrong},
		{blocked, CategoryUnverifiable},
	} {
		var got sql.NullString
		var runID sql.NullInt64
		if err := database.QueryRow(`
SELECT career_site_url_verdict, career_site_url_verdict_run_id FROM companies WHERE id = ?`, tc.id).
			Scan(&got, &runID); err != nil {
			t.Fatalf("read verdict: %v", err)
		}
		if got.String != string(tc.want) {
			t.Errorf("company %d verdict = %q, want %q", tc.id, got.String, tc.want)
		}
		if !runID.Valid || runID.Int64 != sum.RunID {
			t.Errorf("company %d verdict run = %v, want %d", tc.id, runID, sum.RunID)
		}
	}

	var found, inserted, wrongCount, unverifiable int64
	if err := database.QueryRow(`
SELECT items_found, items_inserted, items_wrong, items_unverifiable FROM scrape_runs WHERE id = ?`, sum.RunID).
		Scan(&found, &inserted, &wrongCount, &unverifiable); err != nil {
		t.Fatalf("read run counters: %v", err)
	}
	if found != 3 || inserted != 1 || wrongCount != 1 || unverifiable != 1 {
		t.Errorf("run counters = found %d inserted %d wrong %d unverifiable %d, want 3/1/1/1",
			found, inserted, wrongCount, unverifiable)
	}
}

func TestValidateEscalatesAPageThatNeverRenderedAndAcceptsTheAgentsTarget(t *testing.T) {
	database := newTestDB(t)
	id := seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
	renderer := newFakeRenderer().
		set("https://example.com/careers", browseruse.Result{OK: false, Mode: "render", ErrorKind: browseruse.KindNavigation, Error: "net::ERR_CONNECTION_RESET"}).
		set("https://example.com/company/jobs", careersPage("Careers at Example Corp", 200))
	agent := newFakeRenderer().set("https://example.com/careers", browseruse.Result{
		OK: true, Mode: "agent", FinalURL: "https://example.com/company/jobs", Note: "found the listings link",
	})

	sum := runValidation(t, database, renderer, agent, Options{Escalate: true})

	if sum.Confirmed != 1 || sum.Escalated != 1 {
		t.Fatalf("summary = %+v, want the escalation to confirm the company", sum)
	}

	rows, err := database.Query(`
SELECT source, validation_status, coalesce(rejection_reason,'') FROM url_resolution_attempts
 WHERE company_id = ? ORDER BY attempt_index`, id)
	if err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	defer rows.Close()
	type row struct{ source, status, reason string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.source, &r.status, &r.reason); err != nil {
			t.Fatalf("scan attempt: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("attempts = %d, want 2 (the render and the agent's target)", len(got))
	}
	if got[0].source != SourceRender || got[0].status != "error" || got[0].reason != string(careers.OutcomeTransportError) {
		t.Errorf("first attempt = %+v, want a transport_error render", got[0])
	}
	if got[1].source != SourceAgent || got[1].status != "accepted" {
		t.Errorf("second attempt = %+v, want an accepted browser_use_agent attempt", got[1])
	}
}

// A content verdict is the page's answer. Escalating it would spend a model call to re-ask a
// question that has already been answered, and worse, would invite the agent to overrule the gate.
func TestValidateDoesNotEscalateAContentVerdict(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, "widget-co", "Widget Co", "https://example.com/products")
	renderer := newFakeRenderer().set("https://example.com/products", browseruse.Result{
		OK: true, Mode: "render", FinalURL: "https://example.com/products", Status: 200,
		ContentType: "text/html", Title: "Our Products | Widget Co",
		Body: []byte("<html><title>Our Products | Widget Co</title></html>"),
	})
	agent := newFakeRenderer()

	sum := runValidation(t, database, renderer, agent, Options{Escalate: true})

	if sum.Escalated != 0 {
		t.Errorf("Escalated = %d, want 0", sum.Escalated)
	}
	if agent.callCount() != 0 {
		t.Errorf("the agent ran %d times for a content verdict, want 0", agent.callCount())
	}
	if sum.Wrong != 1 {
		t.Errorf("Wrong = %d, want 1", sum.Wrong)
	}
}

// The plan's adversarial case: an agent that fabricates or wanders to a URL which is not a careers
// page. The gate must judge the second URL on its own merits, and the company must not be relabelled
// "wrong" on the strength of a page the stored URL never claimed to be.
func TestValidateDoesNotLetTheAgentOverruleTheGate(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
	renderer := newFakeRenderer().
		set("https://example.com/careers", browseruse.Result{OK: false, Mode: "render", ErrorKind: browseruse.KindNavigation, Error: "reset"}).
		set("https://example.com/company/careers", browseruse.Result{
			OK: true, Mode: "render", FinalURL: "https://example.com/company/careers", Status: 200,
			ContentType: "text/html", Title: "Careers | Example Corp",
			Body: []byte(`<html><title>Careers | Example Corp</title><body>
<a href="/jobs/1">a</a><a href="/jobs/2">b</a><a href="/jobs/3">c</a>
<a href="/jobs/4">d</a><a href="/jobs/5">e</a></body></html>`),
		})
	agent := newFakeRenderer().set("https://example.com/careers", browseruse.Result{
		OK: true, Mode: "agent", FinalURL: "https://example.com/company/careers",
	})

	sum := runValidation(t, database, renderer, agent, Options{Escalate: true})

	// The second page has a careers signal in its title, so the gate accepts it - the point is that
	// the gate decided, using the same rules it applies everywhere, and that a *rejection* here
	// could not have relabelled the company.
	if sum.Confirmed != 1 {
		t.Fatalf("summary = %+v, want the gate to have judged the agent's URL and accepted it", sum)
	}

	// Now the fabricated case: the agent's URL renders as something else entirely.
	database2 := newTestDB(t)
	seedCompany(t, database2, "example-corp", "Example Corp", "https://example.com/careers")
	renderer2 := newFakeRenderer().
		set("https://example.com/careers", browseruse.Result{OK: false, Mode: "render", ErrorKind: browseruse.KindNavigation, Error: "reset"}).
		set("https://example.com/legal/eeo", browseruse.Result{
			OK: true, Mode: "render", FinalURL: "https://example.com/legal/eeo", Status: 200,
			ContentType: "text/html", Title: "Equal Opportunity | Example Corp",
			Body: []byte("<html><title>Equal Opportunity | Example Corp</title></html>"),
		})
	agent2 := newFakeRenderer().set("https://example.com/careers", browseruse.Result{
		OK: true, Mode: "agent", FinalURL: "https://example.com/legal/eeo",
	})

	sum2 := runValidation(t, database2, renderer2, agent2, Options{Escalate: true})
	if sum2.Unverifiable != 1 || sum2.Wrong != 0 {
		t.Errorf("summary = %+v, want the company left unverifiable rather than relabelled wrong", sum2)
	}
}

func TestValidateFailsCompaniesWhenTheWorkerBreaksProtocol(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
	renderer := newFakeRenderer().fail("https://example.com/careers",
		&browseruse.Error{Kind: browseruse.KindLaunch, Message: "no browser"})

	sum := runValidation(t, database, renderer, nil, Options{})

	if sum.Failed != 1 || sum.Confirmed != 0 {
		t.Fatalf("summary = %+v, want one failure", sum)
	}
	if !strings.Contains(sum.FirstError, "no browser") {
		t.Errorf("FirstError = %q, want the underlying cause", sum.FirstError)
	}

	var status string
	if err := database.QueryRow(`SELECT status FROM scrape_runs WHERE id = ?`, sum.RunID).Scan(&status); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != "error" {
		t.Errorf("run status = %q, want error", status)
	}
}

// A reason the classifier has not been taught must not default to "wrong", and the classification of
// every declared reason is pinned here explicitly rather than restated from contentReasons. If a
// reason moves between classes - or a new one appears - this test fails and the change has to be
// deliberate.
func TestEveryDeclaredReasonIsClassified(t *testing.T) {
	// Rejections that mean "a page was received and it is not a careers page".
	wrongWhenRejected := []careers.Reason{
		careers.OutcomeGenericTitleWithoutCompany,
		careers.OutcomeUnverifiableATSTitle,
		careers.OutcomeProductOrInvestorPath,
		careers.OutcomeLocaleOnlyPath,
		careers.OutcomeNoCareersSignal,
		careers.OutcomeParkedDomain,
		careers.OutcomeATSEmptyBoard,
		careers.OutcomeATSTruncatedBody,
		careers.OutcomeATSInvalidJSON,
	}
	// Everything else that can be rejected leaves the URL unproven: a block, a dead host, a timeout,
	// a body we never read, or a status code that is not an answer about content.
	unverifiableWhenRejected := []careers.Reason{
		careers.OutcomeTransportError,
		careers.OutcomeTimeout,
		careers.OutcomeNoStatus,
		careers.OutcomeForbidden,
		careers.OutcomeRateLimited,
		careers.OutcomeUnparseableFinalURL,
		careers.OutcomeUnknownKind,
		careers.OutcomeBotChallenge,
		careers.OutcomeEmptyBody,
		careers.OutcomeATSHttp4xx,
		careers.OutcomeATSHttp5xx,
		careers.Reason("http_404"),
		careers.Reason("http_500"),
		careers.Reason("http_503"),
	}
	// Reasons that only ever arrive with an accepted status.
	accepted := map[careers.Reason]bool{
		careers.OutcomeHTMLHomepage: true,
		careers.OutcomeHTMLCareers:  true,
		careers.OutcomeATSBoard:     true,
	}

	classified := map[careers.Reason]Category{}
	for _, r := range wrongWhenRejected {
		classified[r] = CategoryWrong
		if got := classify(careers.Verdict{Status: careers.StatusRejected, Reason: r}); got != CategoryWrong {
			t.Errorf("reason %q classified as %q, want wrong", r, got)
		}
	}
	for _, r := range unverifiableWhenRejected {
		if _, claimed := classified[r]; claimed {
			t.Fatalf("reason %q is listed in both classification tables", r)
		}
		classified[r] = CategoryUnverifiable
		if got := classify(careers.Verdict{Status: careers.StatusRejected, Reason: r}); got != CategoryUnverifiable {
			t.Errorf("reason %q classified as %q, want unverifiable", r, got)
		}
	}

	// Every value the gate can actually produce must appear above: a new Reason that nobody
	// classified would otherwise reach the default and be reported as merely unverifiable.
	for _, r := range careers.AllReasons() {
		if accepted[r] {
			if got := classify(careers.Verdict{Status: careers.StatusAccepted, Reason: r}); got != CategoryConfirmed {
				t.Errorf("accepted reason %q classified as %q, want confirmed", r, got)
			}
			continue
		}
		if _, ok := classified[r]; !ok {
			t.Errorf("reason %q is declared but not classified in this test; decide whether it is wrong or unverifiable", r)
		}
	}

	// Both lookup tables must contain only reasons the gate can produce; a stray key is a typo that
	// would otherwise sit there looking load-bearing forever.
	declared := map[careers.Reason]bool{}
	for _, r := range careers.AllReasons() {
		declared[r] = true
	}
	for reason := range contentReasons {
		if !declared[reason] {
			t.Errorf("contentReasons contains %q, which is not a declared Reason", reason)
		}
	}
	for reason := range escalateReasons {
		if !declared[reason] {
			t.Errorf("escalateReasons contains %q, which is not a declared Reason", reason)
		}
	}
}

func TestValidateRetainsEvidenceOnlyForAttemptsThatWereNotAccepted(t *testing.T) {
	dataDir := t.TempDir()

	database := newTestDB(t)
	seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
	renderer := newFakeRenderer().set("https://example.com/careers", careersPage("Careers | Example Corp", 200))
	runValidation(t, database, renderer, nil, Options{DataDir: dataDir})

	if files := evidenceFiles(t, dataDir); len(files) != 0 {
		t.Errorf("accepted attempt wrote evidence %v; an accepted page's verdict is its record", files)
	}

	database2 := newTestDB(t)
	seedCompany(t, database2, "widget-co", "Widget Co", "https://example.com/products")
	renderer2 := newFakeRenderer().set("https://example.com/products", browseruse.Result{
		OK: true, Mode: "render", FinalURL: "https://example.com/products", Status: 200,
		ContentType: "text/html", Title: "Products | Widget Co",
		Body: []byte("<html><title>Products | Widget Co</title><p>buy things</p></html>"),
	})
	runValidation(t, database2, renderer2, nil, Options{DataDir: dataDir})

	files := evidenceFiles(t, dataDir)
	if len(files) != 1 {
		t.Fatalf("evidence files = %v, want exactly one for the rejected attempt", files)
	}
}

func evidenceFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk evidence dir: %v", err)
	}
	return out
}

// The skip rule is only sound while the verdict describes the URL stored now. A resolve run changing
// a company's careers URL must invalidate its old verdict, or the company is skipped forever
// carrying an answer about a page it no longer points at.
func TestLoadStoredCompaniesTreatsAVerdictAboutAnotherURLAsUnvalidated(t *testing.T) {
	database := newTestDB(t)
	id := seedCompany(t, database, "moved-co", "Moved Co", "https://moved.example/old-careers")

	if _, err := database.Exec(`
UPDATE companies SET career_site_url_verdict = 'confirmed',
       career_site_url_verdict_url = 'https://moved.example/old-careers',
       career_site_url_verdict_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id = ?`, id); err != nil {
		t.Fatalf("stamp verdict: %v", err)
	}

	settled, err := LoadStoredCompanies(context.Background(), database, StoredFilter{SkipValidated: true})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	if len(settled) != 0 {
		t.Fatalf("companies = %+v, want none: the verdict is about the stored URL", settled)
	}

	// The resolver repoints the company at a different page.
	if _, err := database.Exec(`
UPDATE companies SET career_site_url = 'https://moved.example/new-careers' WHERE id = ?`, id); err != nil {
		t.Fatalf("repoint company: %v", err)
	}

	moved, err := LoadStoredCompanies(context.Background(), database, StoredFilter{SkipValidated: true})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	if len(moved) != 1 || moved[0].Slug != "moved-co" {
		t.Fatalf("companies = %+v, want moved-co: its verdict is about a URL it no longer stores", moved)
	}
}

func TestLoadStoredCompaniesAppliesOnlySlugs(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, "keep-me", "Keep Me", "https://keep.example/careers")
	seedCompany(t, database, "drop-me", "Drop Me", "https://drop.example/careers")

	got, err := LoadStoredCompanies(context.Background(), database, StoredFilter{OnlySlugs: []string{"keep-me"}})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "keep-me" {
		t.Fatalf("companies = %+v, want only keep-me", got)
	}
}

// The resumability behaviour: a sweep that died halfway can be restarted without re-fetching the
// companies it already settled.
func TestLoadStoredCompaniesPendingOnlySkipsCompaniesWithAVerdict(t *testing.T) {
	database := newTestDB(t)
	validated := seedCompany(t, database, "done-co", "Done Co", "https://done.example/careers")
	seedCompany(t, database, "todo-co", "Todo Co", "https://todo.example/careers")

	none, err := LoadStoredCompanies(context.Background(), database, StoredFilter{SkipValidated: true})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	if len(none) != 2 {
		t.Fatalf("pending companies = %d, want 2 before any verdict exists", len(none))
	}

	if _, err := database.Exec(`
UPDATE companies SET career_site_url_verdict = 'confirmed',
       career_site_url_verdict_url = career_site_url,
       career_site_url_verdict_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id = ?`, validated); err != nil {
		t.Fatalf("stamp verdict: %v", err)
	}

	pending, err := LoadStoredCompanies(context.Background(), database, StoredFilter{SkipValidated: true})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	if len(pending) != 1 || pending[0].Slug != "todo-co" {
		t.Fatalf("pending companies = %+v, want only todo-co", pending)
	}
}

func TestLoadStoredCompaniesStaleAfterTakesNeverValidatedAndOldButNotFresh(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, "never-co", "Never Co", "https://never.example/careers")
	old := seedCompany(t, database, "old-co", "Old Co", "https://old.example/careers")
	fresh := seedCompany(t, database, "fresh-co", "Fresh Co", "https://fresh.example/careers")

	now := time.Now()
	for id, at := range map[int64]string{
		old:   VerdictCutoff(now, 2*time.Hour),
		fresh: now.UTC().Format(VerdictTimestampLayout),
	} {
		if _, err := database.Exec(`
UPDATE companies SET career_site_url_verdict = 'confirmed', career_site_url_verdict_url = career_site_url,
       career_site_url_verdict_at = ? WHERE id = ?`, at, id); err != nil {
			t.Fatalf("stamp verdict: %v", err)
		}
	}

	got, err := LoadStoredCompanies(context.Background(), database, StoredFilter{VerdictCutoff: VerdictCutoff(now, time.Hour)})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	slugs := []string{}
	for _, c := range got {
		slugs = append(slugs, c.Slug)
	}
	if strings.Join(slugs, "|") != "never-co|old-co" {
		t.Errorf("stale companies = %v, want never-co and old-co (fresh-co was validated within the window)", slugs)
	}
}

// The cutoff is compared lexically against a fixed-width column, so its own formatting is part of
// the contract, not an implementation detail.
func TestVerdictCutoffIsFixedWidthUTC(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 30, 45, 123_000_000, time.UTC)
	got := VerdictCutoff(now, 24*time.Hour)
	if got != "2026-09-26T12:30:45.123Z" {
		t.Errorf("VerdictCutoff = %q, want 2026-09-26T12:30:45.123Z", got)
	}
	if len(got) != len(VerdictTimestampLayout) {
		t.Errorf("cutoff %q is not the width of the stored format %q", got, VerdictTimestampLayout)
	}
}

func TestLoadStoredCompaniesIgnoresRowsWithNoUsableURL(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, "has-url", "Has URL", "https://example.com/careers")
	if _, err := database.Exec(`INSERT INTO companies (slug, name, career_site_url) VALUES ('null-url','Null URL',NULL)`); err != nil {
		t.Fatalf("seed NULL-url company: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO companies (slug, name, career_site_url) VALUES ('blank-url','Blank URL','  ')`); err != nil {
		t.Fatalf("seed blank-url company: %v", err)
	}

	got, err := LoadStoredCompanies(context.Background(), database, StoredFilter{})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "has-url" {
		t.Fatalf("companies = %+v, want only the row with a usable URL", got)
	}

	restricted, err := LoadStoredCompanies(context.Background(), database, StoredFilter{OnlySlugs: []string{"null-url"}})
	if err != nil {
		t.Fatalf("LoadStoredCompanies(restricted): %v", err)
	}
	if len(restricted) != 0 {
		t.Errorf("restricted load = %+v, want none: the slug names a row with no usable URL", restricted)
	}
}

func TestValidateAppliesTheLimit(t *testing.T) {
	database := newTestDB(t)
	for i := 0; i < 5; i++ {
		slug := fmt.Sprintf("company-%d", i)
		seedCompany(t, database, slug, "Company "+slug, "https://"+slug+".example.com/careers")
	}
	renderer := newFakeRenderer()
	for i := 0; i < 5; i++ {
		renderer.set(fmt.Sprintf("https://company-%d.example.com/careers", i), careersPage("Careers", 200))
	}

	sum := runValidation(t, database, renderer, nil, Options{Limit: 2})

	if sum.Found != 2 {
		t.Errorf("Found = %d, want 2", sum.Found)
	}
	if renderer.callCount() != 2 {
		t.Errorf("renders = %d, want 2: the limit must be applied before any work", renderer.callCount())
	}
}

func TestValidateRunsConcurrentlyButKeepsEveryAttempt(t *testing.T) {
	database := newTestDB(t)
	// Nine companies at concurrency four, not eight. Eight is the count the old spawn loop happened
	// to survive: with `concurrency + cap(results)` slots it could absorb 8 while blocking, so a
	// deadlock only appeared once one more company remained. A test sized to the boundary is the
	// only one that would have caught it.
	const companies = 9
	for i := 0; i < companies; i++ {
		slug := fmt.Sprintf("company-%d", i)
		seedCompany(t, database, slug, "Company "+slug, "https://"+slug+".example.com/careers")
	}
	renderer := newFakeRenderer()
	for i := 0; i < companies; i++ {
		renderer.set(fmt.Sprintf("https://company-%d.example.com/careers", i), careersPage("Careers", 200))
	}

	sum := runValidationWithin(t, 10*time.Second, database, renderer, nil, Options{Concurrency: 4})

	if sum.Found != companies || sum.Confirmed != companies {
		t.Fatalf("summary = %+v, want %d confirmed", sum, companies)
	}
	var distinctAttempts int
	if err := database.QueryRow(`SELECT count(*) FROM url_resolution_attempts WHERE run_id = ?`, sum.RunID).
		Scan(&distinctAttempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if distinctAttempts != companies {
		t.Errorf("attempts = %d, want %d - one per company, none lost to the pool", distinctAttempts, companies)
	}
}

// The smallest input that deadlocked the original pipeline: three companies at concurrency one.
// Spawning had to finish before draining, and once the single worker blocked on a full one-slot
// results buffer the spawn loop blocked on the semaphore that worker still held.
func TestValidateConcurrencyOneHandlesMoreCompaniesThanItCanBuffer(t *testing.T) {
	database := newTestDB(t)
	for i := 0; i < 3; i++ {
		slug := fmt.Sprintf("company-%d", i)
		seedCompany(t, database, slug, "Company "+slug, "https://"+slug+".example.com/careers")
	}
	renderer := newFakeRenderer()
	for i := 0; i < 3; i++ {
		renderer.set(fmt.Sprintf("https://company-%d.example.com/careers", i), careersPage("Careers", 200))
	}

	sum := runValidationWithin(t, 10*time.Second, database, renderer, nil, Options{Concurrency: 1})

	if sum.Found != 3 || sum.Confirmed != 3 {
		t.Fatalf("summary = %+v, want three confirmed", sum)
	}
}

func TestValidateReportsProgressOncePerCompany(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, "example-corp", "Example Corp", "https://example.com/careers")
	seedCompany(t, database, "widget-co", "Widget Co", "https://example.com/products")
	renderer := newFakeRenderer().
		set("https://example.com/careers", careersPage("Careers | Example Corp", 200)).
		set("https://example.com/products", browseruse.Result{
			OK: true, Mode: "render", FinalURL: "https://example.com/products", Status: 200,
			ContentType: "text/html", Title: "Products | Widget Co",
			Body: []byte("<html><title>Products | Widget Co</title></html>"),
		})

	var seen []Progress
	runValidation(t, database, renderer, nil, Options{Progress: func(p Progress) { seen = append(seen, p) }})

	if len(seen) != 2 {
		t.Fatalf("progress calls = %d, want one per company", len(seen))
	}
	bySlug := map[string]Progress{}
	for _, p := range seen {
		bySlug[p.Slug] = p
	}
	if got := bySlug["example-corp"]; got.Category != CategoryConfirmed || got.HTTPStatus != 200 || got.Reason != "" {
		t.Errorf("example-corp progress = %+v", got)
	}
	if got := bySlug["widget-co"]; got.Category != CategoryWrong || got.Reason != careers.OutcomeProductOrInvestorPath {
		t.Errorf("widget-co progress = %+v", got)
	}
}

// runValidationWithin is runValidation with a deadline. A deadlocked pipeline is the failure mode
// these tests guard against, and a plain call would surface it as a package-wide timeout with no
// indication of which test hung - or, worse, as a CI job that just stops.
func runValidationWithin(t *testing.T, timeout time.Duration, database *sql.DB, renderer, agent browseruse.Renderer, opts Options) Summary {
	t.Helper()
	companies, err := LoadStoredCompanies(context.Background(), database, StoredFilter{OnlySlugs: opts.OnlySlugs})
	if err != nil {
		t.Fatalf("LoadStoredCompanies: %v", err)
	}
	runner := Runner{DB: database, Renderer: renderer, Agent: agent}

	type result struct {
		sum Summary
		err error
	}
	done := make(chan result, 1)
	go func() {
		sum, err := runner.Run(context.Background(), companies, opts)
		done <- result{sum, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run: %v", got.err)
		}
		return got.sum
	case <-time.After(timeout):
		t.Fatalf("Run did not finish within %s: the worker pipeline is deadlocked", timeout)
		return Summary{}
	}
}

// The integration point of the strict profile: the job must request it. This page is the exact shape
// that produced a false "confirmed" in the first sweep - a product called JobStar, whose title
// contains "job" - and the generous discovery rules accept it.
func TestValidateRefusesAPageTheGenerousDiscoveryRulesWouldAccept(t *testing.T) {
	database := newTestDB(t)
	url := "https://www.pricesmart.com/en/catalogsearch/result/?q=JobStar"
	seedCompany(t, database, "pricesmart", "PriceSmart, Inc.", url)
	renderer := newFakeRenderer().set(url, browseruse.Result{
		OK: true, Mode: "render", FinalURL: url, Status: 200,
		ContentType: "text/html; charset=utf-8",
		Title:       "JobStar 2-in-1 Brush Cutter and Trimmer 42.7 cc",
		Body:        []byte(`<html><head><title>JobStar 2-in-1 Brush Cutter and Trimmer 42.7 cc</title></head><body><h1>JobStar</h1><p>Add to cart</p></body></html>`),
	})

	sum := runValidation(t, database, renderer, nil, Options{})

	if sum.Confirmed != 0 || sum.Wrong != 1 {
		t.Fatalf("summary = %+v, want the page refused: this is the false positive the profile exists for", sum)
	}
}

// An accepted page keeps a bounded prefix so a future rule change can re-judge it without re-fetching
// the whole board. A rejected page keeps everything, which is the record a human reads.
func TestValidateRetainsABoundedPrefixOfAcceptedPagesForRejudging(t *testing.T) {
	dataDir := t.TempDir()
	database := newTestDB(t)
	url := "https://example.com/careers"
	seedCompany(t, database, "example-corp", "Example Corp", url)

	body := "<html><head><title>Careers | Example Corp</title></head><body><h1>Open positions</h1>" +
		strings.Repeat(`<a href="/jobs/1">role</a>`, 600) + "</body></html>"
	renderer := newFakeRenderer().set(url, browseruse.Result{
		OK: true, Mode: "render", FinalURL: url, Status: 200,
		ContentType: "text/html; charset=utf-8", Title: "Careers | Example Corp", Body: []byte(body),
	})

	sum := runValidation(t, database, renderer, nil, Options{DataDir: dataDir, EvidenceBodyBytes: 512})
	if sum.Confirmed != 1 {
		t.Fatalf("summary = %+v, want the listing accepted", sum)
	}

	files := evidenceFiles(t, dataDir)
	if len(files) != 1 {
		t.Fatalf("evidence files = %v, want one bounded capture of the accepted page", files)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatalf("stat evidence: %v", err)
	}
	if info.Size() > 512 {
		t.Errorf("evidence is %d bytes, want at most the 512-byte bound", info.Size())
	}
}
