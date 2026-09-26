// ats_test.go: tests for tier 5a, the applicant-tracking JSON boards.
//
// Fixtures are the shapes the three vendors actually return, reduced. No test touches the network.
//
// The truncation case is the one that matters: a body cut mid-object is also invalid JSON, so a
// naive implementation reports it as a malformed response. The two mean different things - an
// oversized board versus a broken one - and the audit trail is only useful if it says which.
package careers

import (
	"context"
	"strings"
	"testing"
)

// singleResponseFetcher returns one canned response for any URL, and records the bound it was given.
type singleResponseFetcher struct {
	resp Response
	url  string
	body int64
}

func (f *singleResponseFetcher) Fetch(_ context.Context, url string, maxBodyBytes int64) (Response, error) {
	f.url = url
	f.body = maxBodyBytes
	r := f.resp
	if r.FinalURL == "" {
		r.FinalURL = url
	}
	return r, nil
}

// resolveWithResponse runs the ATS tier against one canned response.
func resolveWithResponse(t *testing.T, tenant string, resp Response) ATSOutcome {
	t.Helper()
	f := &singleResponseFetcher{resp: resp}
	got, err := (ATSResolver{Fetcher: f}).Resolve(context.Background(), tenant)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return got
}

func jsonResponse(url, body string) Response {
	return Response{FinalURL: url, Status: 200, ContentType: "application/json", Body: []byte(body)}
}

// --- the happy paths ------------------------------------------------------

func TestGreenhouseAPIAcceptsNonEmptyJobs(t *testing.T) {
	f := &singleResponseFetcher{resp: jsonResponse("",
		`{"jobs":[{"id":1,"title":"Engineer"},{"id":2,"title":"Designer"}]}`)}

	got, err := (ATSResolver{Fetcher: f}).Resolve(context.Background(), "https://boards.greenhouse.io/airbnb")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.Accepted {
		t.Fatalf("not accepted: %+v", got)
	}
	if got.JobCount != 2 {
		t.Errorf("JobCount = %d, want 2", got.JobCount)
	}
	if !strings.Contains(f.url, "boards-api.greenhouse.io/v1/boards/airbnb/jobs") {
		t.Errorf("queried %q, want the Greenhouse API endpoint", f.url)
	}
}

// Lever returns a bare array rather than an object, which the other two do not.
func TestLeverAPIAcceptsNonEmptyArray(t *testing.T) {
	f := &singleResponseFetcher{resp: jsonResponse("",
		`[{"id":"a","text":"Engineer"},{"id":"b","text":"Designer"}]`)}

	got, err := (ATSResolver{Fetcher: f}).Resolve(context.Background(), "https://jobs.lever.co/acme")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.Accepted {
		t.Fatalf("not accepted: %+v", got)
	}
	if got.JobCount != 2 {
		t.Errorf("JobCount = %d, want 2", got.JobCount)
	}
	if !strings.Contains(f.url, "api.lever.co/v0/postings/acme") {
		t.Errorf("queried %q", f.url)
	}
	if !strings.Contains(f.url, "mode=json") {
		t.Errorf("queried %q, want mode=json", f.url)
	}
}

func TestAshbyAPIAcceptsNonEmptyJobs(t *testing.T) {
	f := &singleResponseFetcher{resp: jsonResponse("",
		`{"jobs":[{"id":"1","title":"Engineer","jobUrl":"https://jobs.ashbyhq.com/acme/1"}]}`)}

	got, err := (ATSResolver{Fetcher: f}).Resolve(context.Background(), "https://jobs.ashbyhq.com/acme")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.Accepted || got.JobCount != 1 {
		t.Fatalf("got %+v, want one accepted posting", got)
	}
	if !strings.Contains(f.url, "api.ashbyhq.com/posting-api/job-board/acme") {
		t.Errorf("queried %q", f.url)
	}
}

// --- rejection paths ------------------------------------------------------

// A cut body fails the decoder too. It must be reported as truncation, or the audit trail blames
// malformed JSON for what is really an oversized response.
func TestATSTruncatedBodyRejectedNotInvalidJSON(t *testing.T) {
	// The first N bytes of a real board, cut mid-object: syntactically invalid when parsed.
	full := `{"jobs":[{"id":"1","title":"Engineer","description":"` + strings.Repeat("x", 500) + `"}]}`
	cut := full[:200]

	got := resolveWithResponse(t, "https://jobs.ashbyhq.com/acme",
		Response{FinalURL: "https://api.ashbyhq.com/posting-api/job-board/acme",
			Status: 200, ContentType: "application/json", Body: []byte(cut), Truncated: true})

	if got.Accepted {
		t.Fatal("a truncated body was accepted")
	}
	if got.Reason != OutcomeATSTruncatedBody {
		t.Errorf("reason = %q, want %q; a cut body is also invalid JSON and would otherwise be misreported",
			got.Reason, OutcomeATSTruncatedBody)
	}

	// And the same bytes without the flag are a parse error, which is the distinction being made.
	got2 := resolveWithResponse(t, "https://jobs.ashbyhq.com/acme",
		Response{FinalURL: "https://api.ashbyhq.com/posting-api/job-board/acme",
			Status: 200, ContentType: "application/json", Body: []byte(cut)})
	if got2.Reason != OutcomeATSInvalidJSON {
		t.Errorf("reason without the truncation flag = %q, want %q", got2.Reason, OutcomeATSInvalidJSON)
	}
}

func TestATSInvalidJSONRejected(t *testing.T) {
	got := resolveWithResponse(t, "https://jobs.ashbyhq.com/acme",
		jsonResponse("https://api.ashbyhq.com/posting-api/job-board/acme", "{not json at all"))
	if got.Accepted {
		t.Fatal("malformed JSON was accepted")
	}
	if got.Reason != OutcomeATSInvalidJSON {
		t.Errorf("reason = %q, want %q", got.Reason, OutcomeATSInvalidJSON)
	}
}

// The shape a nonexistent company produces. Never acceptable.
func TestATSEmptyBoardRejected(t *testing.T) {
	for _, body := range []string{`{"jobs":[]}`, `[]`, `{"jobs":[]}`, `{"content":[]}`} {
		got := resolveWithResponse(t, "https://jobs.ashbyhq.com/zzzznotrealco999",
			jsonResponse("https://api.ashbyhq.com/posting-api/job-board/zzzznotrealco999", body))
		if got.Accepted {
			t.Errorf("body %q was accepted as a board", body)
			continue
		}
		if got.Reason != OutcomeATSEmptyBoard {
			t.Errorf("body %q: reason = %q, want %q", body, got.Reason, OutcomeATSEmptyBoard)
		}
	}
}

func TestATSHttp4xxRejected(t *testing.T) {
	got := resolveWithResponse(t, "https://boards.greenhouse.io/acme",
		Response{FinalURL: "https://boards-api.greenhouse.io/v1/boards/acme/jobs",
			Status: 404, ContentType: "application/json", Body: []byte(`{"error":"not found"}`)})
	if got.Accepted {
		t.Fatal("a 404 was accepted")
	}
	if got.Reason != OutcomeATSHttp4xx {
		t.Errorf("reason = %q, want %q", got.Reason, OutcomeATSHttp4xx)
	}
}

func TestATSHttp5xxRejected(t *testing.T) {
	got := resolveWithResponse(t, "https://boards.greenhouse.io/acme",
		Response{FinalURL: "https://boards-api.greenhouse.io/v1/boards/acme/jobs",
			Status: 503, ContentType: "text/html", Body: []byte("unavailable")})
	if got.Accepted {
		t.Fatal("a 503 was accepted")
	}
	if got.Reason != OutcomeATSHttp5xx {
		t.Errorf("reason = %q, want %q", got.Reason, OutcomeATSHttp5xx)
	}
}

// 403 and 429 are their own reasons at this tier too, because the remedy differs from a plain 4xx.
func TestATSForbiddenAndRateLimitedKeepTheirOwnReasons(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   Reason
	}{
		{403, OutcomeForbidden},
		{429, OutcomeRateLimited},
	} {
		got := resolveWithResponse(t, "https://jobs.lever.co/acme",
			Response{FinalURL: "https://api.lever.co/v0/postings/acme", Status: tc.status, Body: []byte("x")})
		if got.Reason != tc.want {
			t.Errorf("status %d: reason = %q, want %q", tc.status, got.Reason, tc.want)
		}
	}
}

// A transport failure is an unknown, not a rejection, at this tier as well.
func TestATSTransportFailureIsAnError(t *testing.T) {
	got := resolveWithResponse(t, "https://jobs.lever.co/acme",
		Response{TransportError: errString("connection refused")})
	if got.Reason != OutcomeTransportError {
		t.Errorf("reason = %q, want %q", got.Reason, OutcomeTransportError)
	}
	if got.Accepted {
		t.Error("a transport failure was accepted")
	}
}

// A host this tier cannot read is not an error: it is a board with no JSON API here.
func TestUnknownVendorIsReportedNotFailed(t *testing.T) {
	got, err := (ATSResolver{Fetcher: &singleResponseFetcher{}}).Resolve(
		context.Background(), "https://acme.wd1.myworkdayjobs.com/AcmeCareers")
	if err != nil {
		t.Fatalf("Resolve returned an error for an unreadable vendor: %v", err)
	}
	if got.Accepted {
		t.Error("a Workday tenant was accepted by a tier that has no API for it")
	}
	if got.Reason != OutcomeUnknownKind {
		t.Errorf("reason = %q, want %q", got.Reason, OutcomeUnknownKind)
	}
}

// --- the URL that gets written --------------------------------------------

// career_site_url is the human-facing board, never the API endpoint. This is the guard: the API URL
// is machine-readable and would break the next tier that expects to read a page.
func TestATSTenantURLIsCareerSiteURLNotAPIURL(t *testing.T) {
	f := &singleResponseFetcher{resp: jsonResponse("",
		`{"jobs":[{"id":1}]}`)}

	got, err := (ATSResolver{Fetcher: f}).Resolve(context.Background(), "https://boards.greenhouse.io/airbnb")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.TenantURL != "https://boards.greenhouse.io/airbnb" {
		t.Errorf("TenantURL = %q, want the board URL as given", got.TenantURL)
	}
	if strings.Contains(got.TenantURL, "boards-api") {
		t.Errorf("TenantURL = %q, want the human-facing board rather than the API", got.TenantURL)
	}
	// The API URL is still recorded separately, because the audit trail should say what was read.
	if !strings.Contains(got.APIURL, "boards-api.greenhouse.io") {
		t.Errorf("APIURL = %q, want the endpoint that was queried", got.APIURL)
	}
}

// --- body bounds ----------------------------------------------------------

// Ashby's board measured 13.8MB, so its bound is three times the others'. A shared bound would
// either truncate its boards or make every other call ready to buffer 24MB.
func TestATSFetchUsesAshbyBodyBound(t *testing.T) {
	f := &singleResponseFetcher{resp: jsonResponse("", `{"jobs":[{"id":1}]}`)}
	if _, err := (ATSResolver{Fetcher: f}).Resolve(context.Background(), "https://jobs.ashbyhq.com/acme"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if f.body != ashbyMaxBodyBytes {
		t.Errorf("Ashby bound = %d, want %d", f.body, ashbyMaxBodyBytes)
	}
}

func TestATSFetchUsesGreenhouseBodyBound(t *testing.T) {
	f := &singleResponseFetcher{resp: jsonResponse("", `{"jobs":[{"id":1}]}`)}
	if _, err := (ATSResolver{Fetcher: f}).Resolve(context.Background(), "https://boards.greenhouse.io/acme"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if f.body != greenhouseMaxBodyBytes {
		t.Errorf("Greenhouse bound = %d, want %d", f.body, greenhouseMaxBodyBytes)
	}
}

func TestATSFetchUsesLeverBodyBound(t *testing.T) {
	f := &singleResponseFetcher{resp: jsonResponse("", `[{"id":"a"}]`)}
	if _, err := (ATSResolver{Fetcher: f}).Resolve(context.Background(), "https://jobs.lever.co/acme"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if f.body != leverMaxBodyBytes {
		t.Errorf("Lever bound = %d, want %d", f.body, leverMaxBodyBytes)
	}
}

// --- tenant parsing -------------------------------------------------------

func TestParseTenantExtractsVendorAndSlug(t *testing.T) {
	for _, tc := range []struct {
		url    string
		tenant string
		ats    ATS
	}{
		{"https://boards.greenhouse.io/airbnb", "airbnb", ATSGreenhouse},
		{"https://boards.greenhouse.io/airbnb/jobs", "airbnb", ATSGreenhouse},
		{"https://job-boards.greenhouse.io/openai", "openai", ATSGreenhouse},
		{"https://jobs.lever.co/netflix", "netflix", ATSLever},
		{"https://jobs.ashbyhq.com/openai", "openai", ATSAshby},
		{"https://jobs.ashbyhq.com/openai/careers", "openai", ATSAshby},
		// A per-tenant subdomain still resolves to the vendor.
		{"https://acme.jobs.ashbyhq.com/", "acme", ATSAshby},
	} {
		tenant, ats, err := ParseTenant(tc.url)
		if err != nil {
			t.Errorf("ParseTenant(%q): %v", tc.url, err)
			continue
		}
		if tenant != tc.tenant || ats != tc.ats {
			t.Errorf("ParseTenant(%q) = (%q, %q), want (%q, %q)", tc.url, tenant, ats, tc.tenant, tc.ats)
		}
	}
}

// A URL belonging to no known vendor returns an error rather than a bogus tenant, so the caller can
// tell "not a board" from "a board with no slug".
func TestParseTenantRejectsUnknownHostsAndMissingSlugs(t *testing.T) {
	for _, url := range []string{
		"https://acme.wd1.myworkdayjobs.com/AcmeCareers",
		"https://www.acme.com/careers",
		"https://boards.greenhouse.io/",
		"https://jobs.ashbyhq.com",
		"not a url",
	} {
		if tenant, ats, err := ParseTenant(url); err == nil {
			t.Errorf("ParseTenant(%q) = (%q, %q) with no error, want an error", url, tenant, ats)
		}
	}
}

// --- entry counting -------------------------------------------------------

func TestCountBoardEntriesHandlesAllThreeShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		ats  ATS
		body string
		want int
	}{
		{"greenhouse jobs", ATSGreenhouse, `{"jobs":[{"id":1},{"id":2}]}`, 2},
		{"greenhouse empty", ATSGreenhouse, `{"jobs":[]}`, 0},
		{"lever bare array", ATSLever, `[{"id":"a"}]`, 1},
		{"lever empty array", ATSLever, `[]`, 0},
		{"ashby jobs", ATSAshby, `{"jobs":[{"id":"1"}],"apiVersion":"1"}`, 1},
		{"no jobs key", ATSAshby, `{"apiVersion":"1"}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CountBoardEntries(tc.ats, []byte(tc.body))
			if err != nil {
				t.Fatalf("CountBoardEntries: %v", err)
			}
			if got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}
}

// An empty board and a broken body must stay distinguishable: one is a fact about the company, the
// other is a fact about the response.
func TestCountBoardEntriesDistinguishesEmptyFromBroken(t *testing.T) {
	empty, err := CountBoardEntries(ATSGreenhouse, []byte(`{"jobs":[]}`))
	if err != nil {
		t.Errorf("an empty board returned an error: %v", err)
	}
	if empty != 0 {
		t.Errorf("count = %d, want 0", empty)
	}

	if _, err := CountBoardEntries(ATSGreenhouse, []byte(`{"jobs":`)); err == nil {
		t.Error("a broken body returned no error")
	}
}

// The tier satisfies the same seam as everything else, so the limiter wraps it too.
func TestATSResolverUsesTheSeam(t *testing.T) {
	rec := NewRecordingFetcher()
	f := &singleResponseFetcher{resp: jsonResponse("", `{"jobs":[{"id":1}]}`)}
	if _, err := (ATSResolver{Fetcher: rec.Handler(f)}).Resolve(context.Background(), "https://jobs.ashbyhq.com/acme"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("seam saw %d fetches, want 1", rec.Count())
	}
	if rec.Calls()[0].MaxBodyBytes != ashbyMaxBodyBytes {
		t.Errorf("bound through the seam = %d, want %d", rec.Calls()[0].MaxBodyBytes, ashbyMaxBodyBytes)
	}
}
