// handlers_test.go: black-box tests for the read-only JSON API.
//
// Every test drives the real router over a real migrated SQLite database and
// asserts on the HTTP response, never on handler internals. Decoding is done
// into local types that mirror the documented contract rather than into the
// api package's own types, so a wrong json tag fails the test instead of being
// masked by symmetric encode/decode.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"jobsapp/internal/db"
)

// --- contract mirrors (deliberately independent of internal/api/types.go) ---

type salaryJSON struct {
	MinCents *int64  `json:"min_cents"`
	MaxCents *int64  `json:"max_cents"`
	Currency *string `json:"currency"`
	Period   *string `json:"period"`
}

type jobJSON struct {
	ID             int64      `json:"id"`
	Title          string     `json:"title"`
	CompanyName    string     `json:"company_name"`
	Status         string     `json:"status"`
	EmploymentType *string    `json:"employment_type"`
	LocationText   *string    `json:"location_text"`
	Country        *string    `json:"country"`
	IsRemote       bool       `json:"is_remote"`
	Salary         salaryJSON `json:"salary"`
	PostedAt       *string    `json:"posted_at"`
	FirstSeenAt    string     `json:"first_seen_at"`
	LastSeenAt     string     `json:"last_seen_at"`

	// Detail-only fields. Absent in a list item, so they decode as nil.
	Description    *string `json:"description"`
	ListingURL     string  `json:"listing_url"`
	ApplicationURL *string `json:"application_url"`
	DiscoveryURL   *string `json:"discovery_url"`
}

type listJSON struct {
	Jobs   []jobJSON `json:"jobs"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
	Total  int64     `json:"total"`
}

type errorJSON struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// --- fixtures --------------------------------------------------------------

const (
	companyName  = "Acme Robotics"
	companySlug  = "acme-robotics"
	contentType  = "application/json; charset=utf-8"
	remoteOKJob1 = "Go Engineer"
	remoteOKJob2 = "Staff Platform Engineer"
	remoteOKJob3 = "Backend Engineer"
)

// newTestServer opens an in-memory SQLite database, applies the real embedded
// migrations, seeds one company and three jobs, and returns the real router.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name) VALUES (?, ?, ?)`,
		1, companySlug, companyName,
	); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	// The three rows are chosen so that COALESCE(posted_at, first_seen_at) DESC,
	// id DESC yields 2, 3, 1: id 2 has the newest posted_at, id 3 falls back to
	// its first_seen_at, and id 1 is oldest. id 3 has a NULL description and id 2
	// has all-NULL salary columns, so both null paths are exercised.
	const insertJob = `
INSERT INTO job_listings (
    id, company_id, external_id, application_url, listing_url,
    title, employment_type, is_remote, location_text, country,
    description, salary_min_cents, salary_max_cents, salary_currency, salary_period,
    tags_json, raw_data, posted_at, first_seen_at, last_seen_at, status
) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	for _, seed := range []struct {
		id             int64
		externalID     string
		applicationURL any
		listingURL     string
		title          string
		employmentType any
		isRemote       int64
		locationText   any
		country        any
		description    any
		salaryMin      any
		salaryMax      any
		salaryCurrency any
		salaryPeriod   any
		tagsJSON       any
		rawData        any
		postedAt       any
		firstSeenAt    string
		lastSeenAt     string
		status         string
	}{
		{
			id: 1, externalID: "ext-1", applicationURL: nil,
			listingURL: "https://remoteok.com/jobs/1", title: remoteOKJob1,
			employmentType: "contract", isRemote: 1, locationText: "Berlin",
			country: nil, description: "We build robots.",
			salaryMin: int64(12000000), salaryMax: int64(16000000),
			salaryCurrency: "USD", salaryPeriod: "year",
			tagsJSON: `["go"]`, rawData: `{"secret":"raw-1"}`,
			postedAt: nil, firstSeenAt: "2024-01-01T00:00:00.000Z",
			lastSeenAt: "2024-01-01T00:00:00.000Z", status: "open",
		},
		{
			id: 2, externalID: "ext-2", applicationURL: "https://example.test/apply/2",
			listingURL: "https://remoteok.com/jobs/2", title: remoteOKJob2,
			employmentType: "full_time", isRemote: 0, locationText: "Remote (EU)",
			country: "DE", description: "Newest role.",
			salaryMin: nil, salaryMax: nil, salaryCurrency: nil, salaryPeriod: nil,
			tagsJSON: nil, rawData: `{"secret":"raw-2"}`,
			postedAt: "2024-03-01T00:00:00.000Z", firstSeenAt: "2024-01-02T00:00:00.000Z",
			lastSeenAt: "2024-03-01T00:00:00.000Z", status: "open",
		},
		{
			id: 3, externalID: "ext-3", applicationURL: nil,
			listingURL: "https://remoteok.com/jobs/3", title: remoteOKJob3,
			employmentType: nil, isRemote: 1, locationText: nil,
			country: nil, description: nil,
			salaryMin: nil, salaryMax: nil, salaryCurrency: nil, salaryPeriod: nil,
			tagsJSON: nil, rawData: `{"secret":"raw-3"}`,
			postedAt: nil, firstSeenAt: "2024-02-01T00:00:00.000Z",
			lastSeenAt: "2024-02-01T00:00:00.000Z", status: "closed",
		},
	} {
		if _, err := database.Exec(insertJob,
			seed.id, seed.externalID, seed.applicationURL, seed.listingURL,
			seed.title, seed.employmentType, seed.isRemote, seed.locationText, seed.country,
			seed.description, seed.salaryMin, seed.salaryMax, seed.salaryCurrency, seed.salaryPeriod,
			seed.tagsJSON, seed.rawData, seed.postedAt, seed.firstSeenAt, seed.lastSeenAt, seed.status,
		); err != nil {
			t.Fatalf("seed job %d: %v", seed.id, err)
		}
	}

	return NewRouter(database)
}

// --- helpers ---------------------------------------------------------------

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// requireJSON asserts the documented Content-Type. It is called on every
// response the tests inspect, including errors.
func requireJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != contentType {
		t.Errorf("Content-Type = %q, want %q (body: %s)", got, contentType, rec.Body.String())
	}
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) listJSON {
	t.Helper()
	var got listJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list response %q: %v", rec.Body.String(), err)
	}
	return got
}

func decodeJob(t *testing.T, rec *httptest.ResponseRecorder) jobJSON {
	t.Helper()
	var got jobJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode job response %q: %v", rec.Body.String(), err)
	}
	return got
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorJSON {
	t.Helper()
	var got errorJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error response %q: %v", rec.Body.String(), err)
	}
	return got
}

func requireStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, want, rec.Body.String())
	}
}

func requireError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	requireStatus(t, rec, status)
	requireJSON(t, rec)
	if got := decodeError(t, rec).Error.Code; got != code {
		t.Errorf("error.code = %q, want %q", got, code)
	}
}

func jobTitles(jobs []jobJSON) []string {
	titles := make([]string, 0, len(jobs))
	for _, j := range jobs {
		titles = append(titles, j.Title)
	}
	return titles
}

// --- list endpoint ---------------------------------------------------------

func TestListJobs_ReturnsAllWithEnvelope(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs")

	requireStatus(t, rec, http.StatusOK)
	requireJSON(t, rec)

	got := decodeList(t, rec)
	if got.Total != 3 {
		t.Errorf("total = %d, want 3", got.Total)
	}
	if len(got.Jobs) != 3 {
		t.Fatalf("len(jobs) = %d, want 3 (body: %s)", len(got.Jobs), rec.Body.String())
	}
	if got.Limit != 25 {
		t.Errorf("limit = %d, want the documented default 25", got.Limit)
	}
	if got.Offset != 0 {
		t.Errorf("offset = %d, want 0", got.Offset)
	}
}

func TestListJobs_SortsByPostedOrFirstSeenDesc(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs")
	requireStatus(t, rec, http.StatusOK)

	got := decodeList(t, rec)
	want := []int64{2, 3, 1}
	if len(got.Jobs) != len(want) {
		t.Fatalf("len(jobs) = %d, want %d", len(got.Jobs), len(want))
	}
	for i, wantID := range want {
		if got.Jobs[i].ID != wantID {
			t.Errorf("jobs[%d].id = %d, want %d (order: %v)", i, got.Jobs[i].ID, wantID, jobTitles(got.Jobs))
		}
	}

	// The sort key is COALESCE(posted_at, first_seen_at), so the row without a
	// posted_at must not sort as if it had none.
	if got.Jobs[0].PostedAt == nil {
		t.Error("jobs[0].posted_at is null, want the newest posted timestamp")
	}
	if got.Jobs[1].PostedAt != nil {
		t.Errorf("jobs[1].posted_at = %v, want null (falls back to first_seen_at)", *got.Jobs[1].PostedAt)
	}
}

func TestListJobs_LimitReturnsOneAndKeepsTotal(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs?limit=1")
	requireStatus(t, rec, http.StatusOK)
	requireJSON(t, rec)

	got := decodeList(t, rec)
	if len(got.Jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(got.Jobs))
	}
	if got.Jobs[0].ID != 2 {
		t.Errorf("jobs[0].id = %d, want 2 (the newest row)", got.Jobs[0].ID)
	}
	if got.Total != 3 {
		t.Errorf("total = %d, want 3 (the count is independent of the page)", got.Total)
	}
	if got.Limit != 1 {
		t.Errorf("limit = %d, want the requested 1", got.Limit)
	}
}

func TestListJobs_HonorsOffset(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs?limit=1&offset=1")
	requireStatus(t, rec, http.StatusOK)

	got := decodeList(t, rec)
	if len(got.Jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(got.Jobs))
	}
	if got.Jobs[0].ID != 3 {
		t.Errorf("jobs[0].id = %d, want 3 (the middle row)", got.Jobs[0].ID)
	}
	if got.Offset != 1 {
		t.Errorf("offset = %d, want the requested 1", got.Offset)
	}
}

func TestListJobs_RejectsLimitZero(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs?limit=0")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestListJobs_RejectsLimitOver100(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs?limit=101")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestListJobs_RejectsNonIntegerLimit(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs?limit=abc")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestListJobs_RejectsNegativeOffset(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs?offset=-1")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestListJobs_AcceptsMaxLimit(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs?limit=100")
	requireStatus(t, rec, http.StatusOK)
	requireJSON(t, rec)
	if got := decodeList(t, rec).Limit; got != 100 {
		t.Errorf("limit = %d, want the requested 100", got)
	}
}

func TestListJobs_CompanyNameJoined(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs")
	requireStatus(t, rec, http.StatusOK)

	for _, j := range decodeList(t, rec).Jobs {
		if j.CompanyName != companyName {
			t.Errorf("job %d company_name = %q, want %q", j.ID, j.CompanyName, companyName)
		}
	}
}

func TestListJobs_IsRemoteIsBool(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs")
	requireStatus(t, rec, http.StatusOK)

	byID := map[int64]jobJSON{}
	for _, j := range decodeList(t, rec).Jobs {
		byID[j.ID] = j
	}
	if !byID[1].IsRemote {
		t.Error("job 1 is_remote = false, want true (seeded is_remote = 1)")
	}
	if byID[2].IsRemote {
		t.Error("job 2 is_remote = true, want false (seeded is_remote = 0)")
	}
	// A bool must not be emitted as 0/1: decoding would have failed above.
	if body := rec.Body.String(); !strings.Contains(body, `"is_remote":true`) || !strings.Contains(body, `"is_remote":false`) {
		t.Errorf("body does not carry JSON booleans for is_remote: %s", body)
	}
}

func TestListJobs_NullableColumnsAreNull(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs")
	requireStatus(t, rec, http.StatusOK)

	byID := map[int64]jobJSON{}
	for _, j := range decodeList(t, rec).Jobs {
		byID[j.ID] = j
	}

	job2 := byID[2]
	if job2.Salary.MinCents != nil || job2.Salary.MaxCents != nil || job2.Salary.Currency != nil || job2.Salary.Period != nil {
		t.Errorf("job 2 salary = %+v, want all null", job2.Salary)
	}
	if job2.PostedAt == nil {
		t.Error("job 2 posted_at is null, want the seeded timestamp")
	} else if *job2.PostedAt != "2024-03-01T00:00:00Z" {
		t.Errorf("job 2 posted_at = %q, want RFC3339 normalized to 2024-03-01T00:00:00Z", *job2.PostedAt)
	}

	job3 := byID[3]
	if job3.EmploymentType != nil || job3.LocationText != nil || job3.Country != nil {
		t.Errorf("job 3 nullable strings = %+v/%+v/%+v, want null",
			job3.EmploymentType, job3.LocationText, job3.Country)
	}

	job1 := byID[1]
	if job1.Salary.MinCents == nil || *job1.Salary.MinCents != 12000000 {
		t.Errorf("job 1 salary.min_cents = %v, want 12000000", job1.Salary.MinCents)
	}
	if job1.Salary.Currency == nil || *job1.Salary.Currency != "USD" {
		t.Errorf("job 1 salary.currency = %v, want USD", job1.Salary.Currency)
	}
	if job1.PostedAt != nil {
		t.Errorf("job 1 posted_at = %q, want null", *job1.PostedAt)
	}
	if job1.FirstSeenAt != "2024-01-01T00:00:00Z" {
		t.Errorf("job 1 first_seen_at = %q, want RFC3339 normalized", job1.FirstSeenAt)
	}
}

// The list item must carry exactly the documented fields - no description, no
// URLs, no internal columns.
func TestListJobs_ExactFieldSet(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs")
	requireStatus(t, rec, http.StatusOK)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	requireKeys(t, "envelope", envelope, []string{"jobs", "limit", "offset", "total"})

	var jobs []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["jobs"], &jobs); err != nil {
		t.Fatalf("decode jobs array: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("jobs array is empty, want 3 items")
	}
	requireKeys(t, "list item", jobs[0], []string{
		"id", "title", "company_name", "status", "employment_type",
		"location_text", "country", "is_remote", "salary",
		"posted_at", "first_seen_at", "last_seen_at",
	})

	var salary map[string]json.RawMessage
	if err := json.Unmarshal(jobs[0]["salary"], &salary); err != nil {
		t.Fatalf("decode salary: %v", err)
	}
	requireKeys(t, "salary", salary, []string{"min_cents", "max_cents", "currency", "period"})
}

func TestListJobs_OmitsDescriptionAndInternalFields(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs")
	requireStatus(t, rec, http.StatusOK)

	body := rec.Body.String()
	for _, forbidden := range []string{
		"description", "listing_url", "application_url", "discovery_url",
		"raw_data", "raw-1", "external_id", "company_id", "tags_json",
		"company_application_platform_id", "discovery_platform_id",
		"created_at", "updated_at",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("list response leaks %q: %s", forbidden, body)
		}
	}
}

// --- detail endpoint -------------------------------------------------------

func TestGetJob_ReturnsDetail(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs/1")
	requireStatus(t, rec, http.StatusOK)
	requireJSON(t, rec)

	got := decodeJob(t, rec)
	if got.ID != 1 {
		t.Errorf("id = %d, want 1", got.ID)
	}
	if got.Title != remoteOKJob1 {
		t.Errorf("title = %q, want %q", got.Title, remoteOKJob1)
	}
	if got.CompanyName != companyName {
		t.Errorf("company_name = %q, want %q", got.CompanyName, companyName)
	}
	if got.Description == nil || *got.Description != "We build robots." {
		t.Errorf("description = %v, want the verbatim stored text", got.Description)
	}
	if got.ListingURL != "https://remoteok.com/jobs/1" {
		t.Errorf("listing_url = %q, want the stored listing URL", got.ListingURL)
	}
	if got.ApplicationURL != nil {
		t.Errorf("application_url = %v, want null", *got.ApplicationURL)
	}
	if got.DiscoveryURL != nil {
		t.Errorf("discovery_url = %v, want null", *got.DiscoveryURL)
	}
	if got.EmploymentType == nil || *got.EmploymentType != "contract" {
		t.Errorf("employment_type = %v, want contract", got.EmploymentType)
	}
	if !got.IsRemote {
		t.Error("is_remote = false, want true")
	}
}

func TestGetJob_NullDescriptionMarshalsNull(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs/3")
	requireStatus(t, rec, http.StatusOK)
	requireJSON(t, rec)

	if got := decodeJob(t, rec).Description; got != nil {
		t.Errorf("description = %q, want JSON null", *got)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"description":null`) {
		t.Errorf("body = %s, want an explicit \"description\":null (never an empty string)", body)
	}
}

func TestGetJob_ExactFieldSet(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs/1")
	requireStatus(t, rec, http.StatusOK)

	var detail map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	requireKeys(t, "detail", detail, []string{
		"id", "title", "company_name", "status", "employment_type",
		"location_text", "country", "is_remote", "salary",
		"posted_at", "first_seen_at", "last_seen_at",
		"description", "listing_url", "application_url", "discovery_url",
	})

	for _, forbidden := range []string{
		"raw_data", "raw-1", "external_id", "company_id", "tags_json",
		"company_application_platform_id", "discovery_platform_id",
		"created_at", "updated_at",
	} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("detail response leaks %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestGetJob_NotFound(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs/999999")
	requireError(t, rec, http.StatusNotFound, "not_found")
}

func TestGetJob_BadID(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/jobs/abc")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

// --- routing and error envelope --------------------------------------------

func TestPostNotAllowed(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodPost, "/api/jobs")
	requireError(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")

	if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want JSON, not the ServeMux default plain text", ct)
	}
}

func TestDeleteOnDetailNotAllowed(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodDelete, "/api/jobs/1")
	requireError(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
}

func TestUnknownPathReturns404Envelope(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/unknown")
	requireError(t, rec, http.StatusNotFound, "not_found")
}

func TestUnknownRootPathReturns404Envelope(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/")
	requireError(t, rec, http.StatusNotFound, "not_found")
}

func TestAllResponsesAreJSON(t *testing.T) {
	h := newTestServer(t)
	for _, tc := range []struct {
		name   string
		method string
		target string
	}{
		{"list ok", http.MethodGet, "/api/jobs"},
		{"list bad request", http.MethodGet, "/api/jobs?limit=0"},
		{"detail ok", http.MethodGet, "/api/jobs/1"},
		{"detail not found", http.MethodGet, "/api/jobs/999999"},
		{"detail bad id", http.MethodGet, "/api/jobs/abc"},
		{"method not allowed", http.MethodPost, "/api/jobs"},
		{"unknown path", http.MethodGet, "/api/unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.target)
			requireJSON(t, rec)
		})
	}
}

// requireKeys asserts the JSON object carries exactly the documented keys.
func requireKeys(t *testing.T, what string, obj map[string]json.RawMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(obj))
	for k := range obj {
		got = append(got, k)
	}
	sortedWant := append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(sortedWant)

	missing := difference(sortedWant, got)
	extra := difference(got, sortedWant)
	if len(missing) > 0 {
		t.Errorf("%s is missing fields %v", what, missing)
	}
	if len(extra) > 0 {
		t.Errorf("%s carries undocumented fields %v", what, extra)
	}
}

func difference(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := set[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}
