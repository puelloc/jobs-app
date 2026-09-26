// resolutions_test.go: tests for the resolution persistence layer.
//
// These are the first tests against the new schema, so they exercise the CHECK constraints directly
// rather than only through the Go layer that is supposed to respect them.
package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"jobsapp/internal/careers"
)

// resolutionTestRun starts a run and returns its id, so attempt rows have a parent.
func resolutionTestRun(t *testing.T, database *sql.DB) int64 {
	t.Helper()
	id, _, err := StartRun(context.Background(), database, 23)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return id
}

func seedCompany(t *testing.T, database *sql.DB, id int64, slug string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name) VALUES (?, ?, ?)`, id, slug, slug); err != nil {
		t.Fatalf("seed company %s: %v", slug, err)
	}
}

// acceptedAttempt is the shape the schema requires for an accepted row: no reason.
func acceptedAttempt(url string) ResolutionAttempt {
	return ResolutionAttempt{
		Source:          "nav_anchor",
		CandidateURL:    url,
		Kind:            careers.KindCareerSite,
		HTTPStatus:      200,
		FinalURL:        url,
		Title:           "Careers",
		ValidationState: careers.StatusAccepted,
	}
}

// rejectedAttempt carries a reason, which the schema requires for a non-accepted row.
func rejectedAttempt(url string, reason careers.Reason) ResolutionAttempt {
	return ResolutionAttempt{
		Source:          "nav_anchor",
		CandidateURL:    url,
		Kind:            careers.KindCareerSite,
		HTTPStatus:      403,
		ValidationState: careers.StatusRejected,
		Reason:          reason,
	}
}

// --- the closed vocabularies, extended into the database -------------------

// Every Outcome round-trips through rejection_reason. This is TestEveryReasonIsProducible's
// counterpart at the database boundary: the enum is closed in Go, and the column must accept exactly
// that set and nothing else.
func TestRejectionReasonRoundTripsEveryOutcomeValue(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	ctx := context.Background()
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	for i, reason := range careers.AllReasons() {
		// Accepted outcomes must be written as accepted, rejected ones as rejected; the schema
		// enforces the pairing, so the test asserts it too.
		accepted := reason == careers.OutcomeHTMLCareers || reason == careers.OutcomeHTMLHomepage ||
			reason == careers.OutcomeATSBoard
		var attempt ResolutionAttempt
		if accepted {
			attempt = acceptedAttempt("https://example.com/careers")
			attempt.Reason = reason
			// An accepted attempt carries no reason, so the column is NULL and the outcome is not
			// recoverable from it. That is by design; the acceptance reason is not a rejection.
			attempt.Reason = ""
		} else {
			attempt = rejectedAttempt("https://example.com/careers", reason)
		}

		res := Resolution{
			CompanyID:   1,
			CompanySlug: "acme",
			Attempts:    []ResolutionAttempt{attempt},
		}
		if _, err := writer.Write(ctx, runID, i, res); err != nil {
			t.Fatalf("reason %q (index %d): Write: %v", reason, i, err)
		}

		var got string
		if accepted {
			var nullReason any
			if err := database.QueryRow(
				`SELECT rejection_reason FROM url_resolution_attempts WHERE company_id = 1 AND attempt_index = ?`, i).
				Scan(&nullReason); err != nil {
				t.Fatalf("reason %q: read back: %v", reason, err)
			}
			if nullReason != nil {
				t.Errorf("reason %q: accepted attempt stored rejection_reason %v, want NULL", reason, nullReason)
			}
			continue
		}
		if err := database.QueryRow(
			`SELECT rejection_reason FROM url_resolution_attempts WHERE company_id = 1 AND attempt_index = ?`, i).
			Scan(&got); err != nil {
			t.Fatalf("reason %q: read back: %v", reason, err)
		}
		if got != string(reason) {
			t.Errorf("reason round-trip: stored %q, want %q", got, reason)
		}
	}
}

// An undeclared reason must stop the write rather than becoming a string nobody can query.
func TestUndeclaredRejectionReasonIsRefused(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	res := Resolution{
		CompanyID:   1,
		CompanySlug: "acme",
		Attempts: []ResolutionAttempt{
			rejectedAttempt("https://example.com/careers", careers.Reason("not_a_declared_reason")),
		},
	}
	if _, err := writer.Write(context.Background(), runID, 0, res); err == nil {
		t.Fatal("Write accepted an undeclared rejection reason")
	}

	var n int
	if err := database.QueryRow(`SELECT count(*) FROM url_resolution_attempts`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d attempt rows written despite the refusal, want 0", n)
	}
}

func TestSourceEnumRoundTripsEveryValue(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	ctx := context.Background()
	runID := resolutionTestRun(t, database)

	for i, source := range AllCareerSiteSources() {
		id := int64(i + 1)
		slug := "company-" + string(rune('a'+i))
		seedCompany(t, database, id, slug)

		res := Resolution{
			CompanyID:        id,
			CompanySlug:      slug,
			CareerSiteURL:    "https://example.com/careers",
			CareerSiteSource: source,
			Attempts:         []ResolutionAttempt{acceptedAttempt("https://example.com/careers")},
		}
		if _, err := writer.Write(ctx, runID, 0, res); err != nil {
			t.Fatalf("career site source %q: %v", source, err)
		}

		var got string
		if err := database.QueryRow(
			`SELECT career_site_url_source FROM companies WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read back %q: %v", source, err)
		}
		if got != string(source) {
			t.Errorf("career site source round-trip: stored %q, want %q", got, source)
		}
	}

	for i, source := range AllWebsiteSources() {
		id := int64(100 + i)
		slug := "site-" + string(rune('a'+i))
		seedCompany(t, database, id, slug)

		res := Resolution{
			CompanyID:        id,
			CompanySlug:      slug,
			Website:          "https://example.com/",
			WebsiteSource:    source,
			CareerSiteURL:    "https://example.com/careers",
			CareerSiteSource: CareerSiteSourceAnchorScan,
			Attempts:         []ResolutionAttempt{acceptedAttempt("https://example.com/careers")},
		}
		if _, err := writer.Write(ctx, runID, 0, res); err != nil {
			t.Fatalf("website source %q: %v", source, err)
		}

		var got string
		if err := database.QueryRow(`SELECT website_source FROM companies WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read back %q: %v", source, err)
		}
		if got != string(source) {
			t.Errorf("website source round-trip: stored %q, want %q", got, source)
		}
	}
}

// A source value the layer does not declare is refused before a transaction opens, so the caller
// gets an error naming the company rather than a rollback mid-run.
func TestUndeclaredSourceIsRefused(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	for _, res := range []Resolution{
		{CompanyID: 1, CompanySlug: "acme", CareerSiteURL: "https://x/", CareerSiteSource: CareerSiteSource("made_up"),
			Attempts: []ResolutionAttempt{acceptedAttempt("https://x/")}},
		{CompanyID: 1, CompanySlug: "acme", Website: "https://x/", WebsiteSource: WebsiteSource("made_up"),
			CareerSiteURL: "https://x/c", CareerSiteSource: CareerSiteSourceAnchorScan,
			Attempts: []ResolutionAttempt{acceptedAttempt("https://x/c")}},
		// A source set with no value is equally malformed.
		{CompanyID: 1, CompanySlug: "acme", CareerSiteSource: CareerSiteSourceAnchorScan,
			Attempts: []ResolutionAttempt{acceptedAttempt("https://x/")}},
		{CompanyID: 1, CompanySlug: "acme", CareerSiteURL: "https://x/", CareerSiteSource: CareerSiteSourceAnchorScan,
			WebsiteSource: WebsiteSourceWikipediaInfobox,
			Attempts:      []ResolutionAttempt{acceptedAttempt("https://x/")}},
	} {
		if _, err := writer.Write(context.Background(), runID, 0, res); err == nil {
			t.Errorf("Write accepted a malformed resolution: %+v", res)
		}
	}
}

// --- the schema's own CHECKs ----------------------------------------------

// The Go layer refuses these, but the schema must too: a direct insert is how a future migration or
// a hand-fix would bypass the layer.
func TestSchemaRejectsAcceptedWithReason(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, 1, "acme")

	_, err := database.Exec(`
INSERT INTO url_resolution_attempts
    (company_id, source, candidate_url, candidate_kind, validation_status, rejection_reason)
VALUES (1, 'nav_anchor', 'https://x/', 'career_site', 'accepted', 'forbidden')`)
	if err == nil {
		t.Fatal("the schema accepted an accepted attempt carrying a rejection_reason")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Errorf("error = %v, want a CHECK violation", err)
	}
}

func TestSchemaRejectsRejectedWithoutReason(t *testing.T) {
	database := newTestDB(t)
	seedCompany(t, database, 1, "acme")

	_, err := database.Exec(`
INSERT INTO url_resolution_attempts
    (company_id, source, candidate_url, candidate_kind, validation_status, rejection_reason)
VALUES (1, 'nav_anchor', 'https://x/', 'career_site', 'rejected', NULL)`)
	if err == nil {
		t.Fatal("the schema accepted a rejected attempt with no rejection_reason")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Errorf("error = %v, want a CHECK violation", err)
	}
}

func TestAcceptedAttemptHasNullRejectionReason(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	res := Resolution{
		CompanyID: 1, CompanySlug: "acme",
		CareerSiteURL: "https://example.com/careers", CareerSiteSource: CareerSiteSourceAnchorScan,
		Attempts: []ResolutionAttempt{acceptedAttempt("https://example.com/careers")},
	}
	if _, err := writer.Write(context.Background(), runID, 0, res); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var reason any
	if err := database.QueryRow(
		`SELECT rejection_reason FROM url_resolution_attempts WHERE company_id = 1`).Scan(&reason); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if reason != nil {
		t.Errorf("rejection_reason = %v, want NULL for an accepted attempt", reason)
	}
}

func TestRejectedAttemptHasNonNullRejectionReason(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	res := Resolution{
		CompanyID: 1, CompanySlug: "acme",
		Attempts: []ResolutionAttempt{rejectedAttempt("https://example.com/careers", careers.OutcomeForbidden)},
	}
	if _, err := writer.Write(context.Background(), runID, 0, res); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var reason string
	if err := database.QueryRow(
		`SELECT rejection_reason FROM url_resolution_attempts WHERE company_id = 1`).Scan(&reason); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if reason != string(careers.OutcomeForbidden) {
		t.Errorf("rejection_reason = %q, want %q", reason, careers.OutcomeForbidden)
	}
}

// Every attempt is recorded, accepted or not. A trail of only the winners cannot explain a bad pick.
func TestAttemptsWrittenForRejectedCandidatesToo(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	res := Resolution{
		CompanyID: 1, CompanySlug: "acme",
		CareerSiteURL: "https://example.com/careers", CareerSiteSource: CareerSiteSourceAnchorScan,
		Attempts: []ResolutionAttempt{
			rejectedAttempt("https://example.com/jobs", careers.OutcomeNoCareersSignal),
			rejectedAttempt("https://example.com/about", careers.OutcomeBotChallenge),
			acceptedAttempt("https://example.com/careers"),
		},
	}
	out, err := writer.Write(context.Background(), runID, 0, res)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if out.AttemptsInserted != 3 {
		t.Errorf("AttemptsInserted = %d, want 3", out.AttemptsInserted)
	}

	var rejected int
	if err := database.QueryRow(
		`SELECT count(*) FROM url_resolution_attempts WHERE validation_status = 'rejected'`).Scan(&rejected); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rejected != 2 {
		t.Errorf("rejected attempts stored = %d, want 2", rejected)
	}
}

// --- run bookkeeping ------------------------------------------------------

// The run row exists before any attempt, which is what makes run_id resolvable and what a mid-run
// kill leaves behind as evidence.
func TestRunRowWrittenBeforeAnyAttempt(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	// The run row is committed at start, before the writer is touched.
	var status string
	if err := database.QueryRow(`SELECT status FROM scrape_runs WHERE id = ?`, runID).Scan(&status); err != nil {
		t.Fatalf("run row missing before any write: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want running", status)
	}

	if _, err := writer.Write(context.Background(), runID, 0, Resolution{
		CompanyID: 1, CompanySlug: "acme",
		Attempts: []ResolutionAttempt{rejectedAttempt("https://x/", careers.OutcomeForbidden)},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var attempts, runs int
	database.QueryRow(`SELECT count(*) FROM url_resolution_attempts`).Scan(&attempts)
	database.QueryRow(`SELECT count(*) FROM scrape_runs`).Scan(&runs)
	if attempts != 1 || runs != 1 {
		t.Errorf("attempts=%d runs=%d, want 1 and 1", attempts, runs)
	}
}

// The counters mean what section 4.3 says they mean, so a later reader does not have to guess.
func TestRunCountersMatchDefinition(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	ctx := context.Background()
	runID := resolutionTestRun(t, database)

	seedCompany(t, database, 1, "newly-resolved")
	seedCompany(t, database, 2, "re-resolved")
	seedCompany(t, database, 3, "still-unresolved")

	// Company 2 already had a different URL, so it should count as an update.
	if _, err := database.Exec(
		`UPDATE companies SET career_site_url = 'https://old.example.com/careers' WHERE id = 2`); err != nil {
		t.Fatalf("seed existing URL: %v", err)
	}

	cases := []struct {
		res    Resolution
		insert bool
		update bool
	}{
		{Resolution{CompanyID: 1, CompanySlug: "newly-resolved",
			CareerSiteURL: "https://one.example.com/careers", CareerSiteSource: CareerSiteSourceAnchorScan,
			Attempts: []ResolutionAttempt{acceptedAttempt("https://one.example.com/careers")}}, true, false},
		{Resolution{CompanyID: 2, CompanySlug: "re-resolved",
			CareerSiteURL: "https://two.example.com/careers", CareerSiteSource: CareerSiteSourceSitemap,
			Attempts: []ResolutionAttempt{acceptedAttempt("https://two.example.com/careers")}}, false, true},
		{Resolution{CompanyID: 3, CompanySlug: "still-unresolved",
			Attempts: []ResolutionAttempt{rejectedAttempt("https://three.example.com/careers", careers.OutcomeForbidden)}}, false, false},
	}

	var inserted, updated int
	for _, tc := range cases {
		out, err := writer.Write(ctx, runID, 0, tc.res)
		if err != nil {
			t.Fatalf("%s: Write: %v", tc.res.CompanySlug, err)
		}
		if out.CareerSiteInserted != tc.insert {
			t.Errorf("%s: CareerSiteInserted = %v, want %v", tc.res.CompanySlug, out.CareerSiteInserted, tc.insert)
		}
		if out.CareerSiteUpdated != tc.update {
			t.Errorf("%s: CareerSiteUpdated = %v, want %v", tc.res.CompanySlug, out.CareerSiteUpdated, tc.update)
		}
		if out.CareerSiteInserted {
			inserted++
		}
		if out.CareerSiteUpdated {
			updated++
		}
	}

	// items_found is the companies processed; inserted and updated are as counted above.
	if err := FinishRun(ctx, database, runID, "ok", int64(len(cases)), int64(inserted), int64(updated), nil); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	var found, ins, upd int
	if err := database.QueryRow(
		`SELECT items_found, items_inserted, items_updated FROM scrape_runs WHERE id = ?`, runID).
		Scan(&found, &ins, &upd); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if found != 3 || ins != 1 || upd != 1 {
		t.Errorf("counters = found:%d inserted:%d updated:%d, want 3/1/1", found, ins, upd)
	}
}

// --- failure isolation ----------------------------------------------------

// One company's failure must roll back that company alone. A run that aborts on the first bad row
// would make a 1,500-company pass unusable.
func TestFailedCompanyTransactionDoesNotAbortRun(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	ctx := context.Background()
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "good")
	seedCompany(t, database, 2, "bad")

	// The first company succeeds.
	if _, err := writer.Write(ctx, runID, 0, Resolution{
		CompanyID: 1, CompanySlug: "good",
		CareerSiteURL: "https://good.example.com/careers", CareerSiteSource: CareerSiteSourceAnchorScan,
		Attempts: []ResolutionAttempt{acceptedAttempt("https://good.example.com/careers")},
	}); err != nil {
		t.Fatalf("first company: %v", err)
	}

	// The second references a platform that is a reference source, not an application system, so
	// the app-enforced invariant refuses it mid-transaction.
	if _, err := writer.Write(ctx, runID, 0, Resolution{
		CompanyID: 2, CompanySlug: "bad",
		CareerSiteURL: "https://bad.example.com/careers", CareerSiteSource: CareerSiteSourceATSAPI,
		ATSPlatformID: 20, // wikipedia_sp500, platform_type = 'reference'
		ATSBaseURL:    "https://bad.example.com/",
		Attempts:      []ResolutionAttempt{acceptedAttempt("https://bad.example.com/careers")},
	}); err == nil {
		t.Fatal("the writer accepted a reference platform as an application system")
	}

	// The first company's work survived, and the second left nothing behind.
	var goodURL, goodSource string
	if err := database.QueryRow(
		`SELECT career_site_url, career_site_url_source FROM companies WHERE id = 1`).Scan(&goodURL, &goodSource); err != nil {
		t.Fatalf("read company 1: %v", err)
	}
	if goodURL != "https://good.example.com/careers" {
		t.Errorf("company 1 career_site_url = %q, want it kept", goodURL)
	}

	var badURL *string
	if err := database.QueryRow(`SELECT career_site_url FROM companies WHERE id = 2`).Scan(&badURL); err != nil {
		t.Fatalf("read company 2: %v", err)
	}
	if badURL != nil {
		t.Errorf("company 2 career_site_url = %q, want NULL after its rollback", *badURL)
	}
	var badAttempts int
	if err := database.QueryRow(
		`SELECT count(*) FROM url_resolution_attempts WHERE company_id = 2`).Scan(&badAttempts); err != nil {
		t.Fatalf("count: %v", err)
	}
	if badAttempts != 0 {
		t.Errorf("company 2 left %d attempt rows, want 0 after rollback", badAttempts)
	}
}

// The application-system invariant has its own test because SQLite cannot declare it.
func TestInvariantApplicationSystemPlatformEnforced(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	for _, platformID := range []int64{1, 20} { // job board, reference source
		_, err := writer.Write(context.Background(), runID, 0, Resolution{
			CompanyID: 1, CompanySlug: "acme",
			CareerSiteURL: "https://acme.example.com/careers", CareerSiteSource: CareerSiteSourceATSAPI,
			ATSPlatformID: platformID, ATSBaseURL: "https://acme.example.com/",
			Attempts: []ResolutionAttempt{acceptedAttempt("https://acme.example.com/careers")},
		})
		if err == nil {
			t.Errorf("platform %d was accepted as an application system", platformID)
		}
	}

	// A genuine application system works and upserts without duplicating.
	if _, err := writer.Write(context.Background(), runID, 0, Resolution{
		CompanyID: 1, CompanySlug: "acme",
		CareerSiteURL: "https://boards.greenhouse.io/acme", CareerSiteSource: CareerSiteSourceATSAPI,
		ATSPlatformID: 10, ATSBaseURL: "https://boards.greenhouse.io/acme",
		Attempts: []ResolutionAttempt{acceptedAttempt("https://boards.greenhouse.io/acme")},
	}); err != nil {
		t.Fatalf("greenhouse platform was refused: %v", err)
	}

	runID2 := resolutionTestRun(t, database)
	if _, err := writer.Write(context.Background(), runID2, 0, Resolution{
		CompanyID: 1, CompanySlug: "acme",
		CareerSiteURL: "https://boards.greenhouse.io/acme", CareerSiteSource: CareerSiteSourceATSAPI,
		ATSPlatformID: 10, ATSBaseURL: "https://boards.greenhouse.io/acme",
		Attempts: []ResolutionAttempt{acceptedAttempt("https://boards.greenhouse.io/acme")},
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	var n int
	if err := database.QueryRow(
		`SELECT count(*) FROM company_application_platforms WHERE company_id = 1`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("company_application_platforms rows = %d, want 1 after two writes", n)
	}
}

// --- evidence paths -------------------------------------------------------

func TestEvidencePathUsesRunIDNotDate(t *testing.T) {
	got := EvidencePath("/data", "acme-corp", 42, 3, "jobs.acme.com", "html")
	want := filepath.Join("/data", "raw", "careers", "acme-corp", "42", "3-jobs.acme.com.html")
	if got != want {
		t.Errorf("EvidencePath = %q, want %q", got, want)
	}
	if strings.Contains(got, "2026") || strings.Contains(got, "-0") && strings.Count(got, "-") > 3 {
		// A date in the path would collide on a same-day rerun, which is the whole reason the run id
		// is used instead.
		t.Errorf("EvidencePath %q looks like it contains a date", got)
	}
}

func TestEvidencePathIsSafeForUntrustedParts(t *testing.T) {
	for _, tc := range []struct{ slug, host string }{
		{"../../etc/passwd", "evil.example.com"},
		{"a/b", "a/b"},
		{"..", ".."},
		{"", ""},
	} {
		got := EvidencePath("/data", tc.slug, 1, 0, tc.host, "html")
		clean := filepath.Clean(got)
		if !strings.HasPrefix(clean, filepath.Join("/data", "raw", "careers")) {
			t.Errorf("EvidencePath(%q, %q) escaped the evidence root: %q", tc.slug, tc.host, clean)
		}
		if strings.Contains(clean, "..") {
			t.Errorf("EvidencePath(%q, %q) contains a parent reference: %q", tc.slug, tc.host, clean)
		}
	}
}

// The same run slot must not be written twice; that is a writer bug, not a rerun.
func TestSameRunSameAttemptIndexCollides(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	ctx := context.Background()
	runID := resolutionTestRun(t, database)
	seedCompany(t, database, 1, "acme")

	res := Resolution{
		CompanyID: 1, CompanySlug: "acme",
		CareerSiteURL: "https://acme.example.com/careers", CareerSiteSource: CareerSiteSourceAnchorScan,
		Attempts: []ResolutionAttempt{acceptedAttempt("https://acme.example.com/careers")},
	}
	if _, err := writer.Write(ctx, runID, 0, res); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// The evidence path for that slot is now taken, and the unique index refuses a second row.
	path := EvidencePath(t.TempDir(), "acme", runID, 0, "acme.example.com", "html")
	if _, err := WriteEvidence(path, []byte("<html></html>")); err != nil {
		t.Fatalf("first evidence write: %v", err)
	}
	if _, err := WriteEvidence(path, []byte("<html></html>")); !errors.Is(err, ErrEvidenceExists) {
		t.Errorf("second evidence write error = %v, want ErrEvidenceExists", err)
	}

	if _, err := writer.Write(ctx, runID, 0, res); err == nil {
		t.Fatal("the writer accepted the same run slot twice")
	}
}

func TestWriteEvidenceCreatesDirectoriesAndStoresBytes(t *testing.T) {
	path := EvidencePath(t.TempDir(), "acme", 7, 1, "acme.example.com", "json")
	body := []byte(`{"jobs":[]}`)

	got, err := WriteEvidence(path, body)
	if err != nil {
		t.Fatalf("WriteEvidence: %v", err)
	}
	if got != path {
		t.Errorf("returned %q, want %q", got, path)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(stored) != string(body) {
		t.Errorf("stored %q, want %q", stored, body)
	}
}

// The writer is a serialization point, and this pins that.
//
// The shape the run uses is: workers fetch and validate concurrently, results flow into a channel,
// and one goroutine writes. A design where each worker wrote would contend on the single pooled
// connection and could interleave one company's attempts with another's. Here four producers run
// concurrently while a single writer consumes, and every company's rows must land whole.
func TestWriterSerializesConcurrentResults(t *testing.T) {
	database := newTestDB(t)
	writer := NewResolutionWriter(database)
	runID := resolutionTestRun(t, database)

	const companies = 8
	for i := 0; i < companies; i++ {
		seedCompany(t, database, int64(i+1), "company-"+string(rune('a'+i)))
	}

	results := make(chan Resolution)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for res := range results {
				if _, err := writer.Write(context.Background(), runID, 0, res); err != nil {
					t.Errorf("%s: %v", res.CompanySlug, err)
				}
			}
		}()
	}

	for i := 0; i < companies; i++ {
		slug := "company-" + string(rune('a'+i))
		url := "https://" + slug + ".example.com/careers"
		results <- Resolution{
			CompanyID:        int64(i + 1),
			CompanySlug:      slug,
			CareerSiteURL:    url,
			CareerSiteSource: CareerSiteSourceAnchorScan,
			Attempts: []ResolutionAttempt{
				rejectedAttempt("https://"+slug+".example.com/about", careers.OutcomeNoCareersSignal),
				acceptedAttempt(url),
			},
		}
	}
	close(results)
	wg.Wait()

	// One writer consuming serially is the design; the comment above says so, and here that means
	// each company has its own two attempts and nothing is duplicated or dropped.
	var attempts int
	if err := database.QueryRow(`SELECT count(*) FROM url_resolution_attempts`).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != companies*2 {
		t.Errorf("attempt rows = %d, want %d", attempts, companies*2)
	}

	var resolved int
	if err := database.QueryRow(
		`SELECT count(*) FROM companies WHERE career_site_url IS NOT NULL`).Scan(&resolved); err != nil {
		t.Fatalf("count resolved: %v", err)
	}
	if resolved != companies {
		t.Errorf("resolved companies = %d, want %d", resolved, companies)
	}

	// Each company's attempts are contiguous and ordered, which interleaving would break.
	for i := 0; i < companies; i++ {
		var first, second string
		if err := database.QueryRow(`
SELECT
  (SELECT rejection_reason FROM url_resolution_attempts WHERE company_id = ? AND attempt_index = 0),
  (SELECT validation_status FROM url_resolution_attempts WHERE company_id = ? AND attempt_index = 1)`,
			i+1, i+1).Scan(&first, &second); err != nil {
			t.Fatalf("company %d: %v", i+1, err)
		}
		if first != string(careers.OutcomeNoCareersSignal) {
			t.Errorf("company %d attempt 0 reason = %q, want the rejection first", i+1, first)
		}
		if second != string(careers.StatusAccepted) {
			t.Errorf("company %d attempt 1 status = %q, want the acceptance second", i+1, second)
		}
	}
}
