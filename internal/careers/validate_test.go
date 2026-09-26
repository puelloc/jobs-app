// validate_test.go: tests for the validation gate.
//
// The cases come from measurement during planning: the fake-slug responses that returned HTTP 200,
// the decoy links in real navigation, and the redirects that reveal an applicant-tracking vendor.
// No test touches the network.
package careers

import "testing"

// fakeSlugAshby is what jobs.ashbyhq.com serves for a company that does not exist: HTTP 200 with a
// generic title and no jobs. A status-only gate accepts this.
const fakeSlugAshby = `<html><head><title>Jobs</title></head><body><div id="app"></div></body></html>`

// fakeSlugSmartRecruiters answers the same way for a nonexistent company.
const fakeSlugSmartRecruiters = `<html><head><title>SmartRecruiters Job Search</title></head>` +
	`<body><h1>SmartRecruiters Job Search</h1></body></html>`

// realAshby is the shape of a real board: a company-specific title and JobPosting markup.
const realAshby = `<html><head><title>OpenAI Jobs</title>` +
	`<script type="application/ld+json">{"@type":"JobPosting","title":"Engineer"}</script>` +
	`<script type="application/ld+json">{"@type":"JobPosting","title":"Designer"}</script>` +
	`</head><body><h1>OpenAI</h1></body></html>`

// realGreenhouse is the recorded Airbnb board.
const realGreenhouse = `<html><head><title>Positions Archive - Careers at Airbnb</title></head>` +
	`<body><h1>Careers at Airbnb</h1><a href="/positions/1">See all jobs</a></body></html>`

// salesforceDecoy is the product page that competes with the real careers page in Salesforce's
// navigation. Rejecting it needs more than the word "Career" in the title.
const salesforceDecoy = `<html><head><title>Career Paths | Trailhead</title></head>` +
	`<body><h1>Build your career path</h1><p>Learn Salesforce skills.</p></body></html>`

// salesforceReal is the page the anchor scan should pick instead.
const salesforceReal = `<html><head><title>Salesforce Careers</title></head>` +
	`<body><h1>Join our team</h1><a href="/company/careers/university/">University Recruiting</a>` +
	`<p>Search jobs</p></body></html>`

func okResponse(url, title, body string) Response {
	return Response{
		FinalURL:    url,
		Status:      200,
		ContentType: "text/html; charset=utf-8",
		Body:        []byte("<html><head><title>" + title + "</title></head><body>" + body + "</body></html>"),
	}
}

func htmlResponse(url, html string) Response {
	return Response{FinalURL: url, Status: 200, ContentType: "text/html; charset=utf-8", Body: []byte(html)}
}

// --- the fake-slug trap ---------------------------------------------------

func TestValidationGateRejectsAshby200WithoutJobs(t *testing.T) {
	// No company name: the caller has nothing to check the title against, so the board cannot be
	// verified at all.
	v := Validate(Candidate{URL: "https://jobs.ashbyhq.com/zzzznotrealco999", Kind: KindATSBoard, Source: "ats_api"},
		htmlResponse("https://jobs.ashbyhq.com/zzzznotrealco999", fakeSlugAshby))
	if v.Accepted() {
		t.Fatalf("accepted a 200 response with no jobs; verdict = %+v", v)
	}
	if v.Reason != "unverifiable_ats_title" {
		t.Errorf("reason = %q, want unverifiable_ats_title", v.Reason)
	}
}

// With a company name supplied, a generic vendor page still fails the token check, and the reason
// says so specifically.
func TestValidationGateRejectsGenericTitleWhenTheCompanyIsAbsent(t *testing.T) {
	v := Validate(Candidate{
		URL: "https://jobs.ashbyhq.com/zzzznotrealco999", Kind: KindATSBoard,
		CompanyName: "Northwind Traders", Source: "ats_api",
	}, htmlResponse("https://jobs.ashbyhq.com/zzzznotrealco999", fakeSlugAshby))
	if v.Accepted() {
		t.Fatalf("accepted a generic board for a company it does not name; verdict = %+v", v)
	}
	if v.Reason != "generic_title_without_company" {
		t.Errorf("reason = %q, want generic_title_without_company", v.Reason)
	}
}

func TestValidationGateRejectsSmartRecruiters200WithoutCompanyToken(t *testing.T) {
	v := Validate(Candidate{
		URL: "https://jobs.smartrecruiters.com/zzzznotrealco999", Kind: KindATSBoard,
		CompanyName: "Northwind Traders", Source: "ats_api",
	}, htmlResponse("https://jobs.smartrecruiters.com/zzzznotrealco999", fakeSlugSmartRecruiters))
	if v.Accepted() {
		t.Fatalf("accepted a generic SmartRecruiters page; verdict = %+v", v)
	}
	if v.Reason != "generic_title_without_company" {
		t.Errorf("reason = %q, want generic_title_without_company", v.Reason)
	}
}

// The same generic-title check must not reject a real board, whose title names the company.
func TestValidationGateAcceptsRealATSWithNonEmptyJobs(t *testing.T) {
	v := Validate(Candidate{URL: "https://jobs.ashbyhq.com/openai", Kind: KindATSBoard, CompanyName: "OpenAI", Source: "ats_api"},
		htmlResponse("https://jobs.ashbyhq.com/openai", realAshby))
	if !v.Accepted() {
		t.Fatalf("rejected a real board: %+v", v)
	}
	if v.Title != "OpenAI Jobs" {
		t.Errorf("title = %q, want OpenAI Jobs", v.Title)
	}
}

func TestValidationGateAcceptsGreenhouseBoard(t *testing.T) {
	v := Validate(Candidate{URL: "https://boards.greenhouse.io/airbnb", Kind: KindATSBoard, CompanyName: "Airbnb", Source: "ats_api"},
		htmlResponse("https://boards.greenhouse.io/airbnb", realGreenhouse))
	if !v.Accepted() {
		t.Fatalf("rejected the recorded Airbnb board: %+v", v)
	}
}

// An ATS JSON board is accepted only when it lists something.
func TestValidationGateAcceptsATSJSONWithEntries(t *testing.T) {
	body := `{"jobs":[{"id":1,"title":"Engineer"},{"id":2,"title":"Designer"}]}`
	v := Validate(Candidate{URL: "https://boards-api.greenhouse.io/v1/boards/airbnb/jobs", Kind: KindATSBoard, Source: "ats_api"},
		Response{FinalURL: "https://boards-api.greenhouse.io/v1/boards/airbnb/jobs", Status: 200,
			ContentType: "application/json", Body: []byte(body)})
	if !v.Accepted() {
		t.Fatalf("rejected a populated JSON board: %+v", v)
	}
	if v.Evidence != "ats_entries=2" {
		t.Errorf("evidence = %q, want ats_entries=2", v.Evidence)
	}
}

func TestValidationGateRejectsATSJSONWithNoEntries(t *testing.T) {
	v := Validate(Candidate{URL: "https://boards-api.greenhouse.io/v1/boards/zzz/jobs", Kind: KindATSBoard, Source: "ats_api"},
		Response{FinalURL: "https://boards-api.greenhouse.io/v1/boards/zzz/jobs", Status: 200,
			ContentType: "application/json", Body: []byte(`{"jobs":[]}`)})
	if v.Accepted() {
		t.Fatalf("accepted an empty JSON board: %+v", v)
	}
	if v.Reason != "ats_board_empty" {
		t.Errorf("reason = %q, want ats_board_empty", v.Reason)
	}
}

// --- decoys ---------------------------------------------------------------

// The Salesforce navigation offers a product page whose title contains "Career" alongside the real
// careers page. Accepting the decoy is a wrong pick that a status check cannot catch.
func TestValidationGateRejectsProductCareerPathPage(t *testing.T) {
	v := Validate(Candidate{
		URL: "https://trailhead.salesforce.com/career-path/", Kind: KindCareerSite,
		CompanyName: "Salesforce", Source: "nav_anchor",
	}, htmlResponse("https://trailhead.salesforce.com/career-path/", salesforceDecoy))
	if v.Accepted() {
		t.Fatalf("accepted the Trailhead product page as a careers site: %+v", v)
	}
}

func TestValidationGateAcceptsTheRealCareersPageBesideADecoy(t *testing.T) {
	v := Validate(Candidate{
		URL: "https://www.salesforce.com/company/careers/", Kind: KindCareerSite,
		CompanyName: "Salesforce", Source: "nav_anchor",
	}, htmlResponse("https://www.salesforce.com/company/careers/", salesforceReal))
	if !v.Accepted() {
		t.Fatalf("rejected the real Salesforce careers page: %+v", v)
	}
}

// An <a href="#"> is a navigation placeholder. Resolving it against the page would accept the
// homepage as its own careers link.
func TestCareersAnchorScanIgnoresHrefHashDecoy(t *testing.T) {
	if _, err := resolveURL("https://www.lockheedmartin.com/", "#"); err == nil {
		t.Error("resolveURL accepted a bare fragment href")
	}
	if _, err := resolveURL("https://www.lockheedmartin.com/", ""); err == nil {
		t.Error("resolveURL accepted an empty href")
	}
	if _, err := resolveURL("https://www.lockheedmartin.com/", "javascript:void(0)"); err == nil {
		t.Error("resolveURL accepted a javascript: href")
	}
	if _, err := resolveURL("https://www.lockheedmartin.com/", "mailto:jobs@example.com"); err == nil {
		t.Error("resolveURL accepted a mailto: href")
	}
}

func TestResolveURLJoinsRelativeAndAbsoluteCandidates(t *testing.T) {
	for _, tc := range []struct{ base, candidate, want string }{
		{"https://www.costco.com/", "/jobs.html", "https://www.costco.com/jobs.html"},
		{"https://www.costco.com/", "jobs.html", "https://www.costco.com/jobs.html"},
		{"https://www.costco.com/", "https://jobs.nike.com/", "https://jobs.nike.com/"},
		{"https://www.costco.com/", "/careers#openings", "https://www.costco.com/careers"},
	} {
		got, err := resolveURL(tc.base, tc.candidate)
		if err != nil {
			t.Errorf("resolveURL(%q, %q): %v", tc.base, tc.candidate, err)
			continue
		}
		if got != tc.want {
			t.Errorf("resolveURL(%q, %q) = %q, want %q", tc.base, tc.candidate, got, tc.want)
		}
	}
}

// --- transport and status -------------------------------------------------

// A site that cannot be reached is unknown, not disproven, so it must not be recorded as a
// negative result that stops a retry.
func TestValidationGateTreatsTransportFailureAsError(t *testing.T) {
	v := Validate(Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		Response{TransportError: errString("connection refused")})
	if v.Status != StatusError {
		t.Errorf("status = %q, want %q", v.Status, StatusError)
	}
	if v.Reason != "transport_error" {
		t.Errorf("reason = %q", v.Reason)
	}
}

func TestValidationGateRejectsForbiddenAndRateLimited(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{403, "forbidden"},
		{429, "rate_limited"},
		{404, "http_404"},
		{500, "http_500"},
	} {
		v := Validate(Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
			Response{FinalURL: "https://example.com/careers", Status: tc.status, ContentType: "text/html", Body: []byte("<html></html>")})
		if v.Accepted() {
			t.Errorf("status %d was accepted", tc.status)
			continue
		}
		if v.Reason != tc.want {
			t.Errorf("status %d reason = %q, want %q", tc.status, v.Reason, tc.want)
		}
	}
}

func TestValidationGateRejectsEmptyBody(t *testing.T) {
	v := Validate(Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		Response{FinalURL: "https://example.com/careers", Status: 200, ContentType: "text/html", Body: []byte("   ")})
	if v.Accepted() {
		t.Fatalf("accepted an empty body: %+v", v)
	}
	if v.Reason != "empty_body" {
		t.Errorf("reason = %q, want empty_body", v.Reason)
	}
}

// --- bot walls and parking ------------------------------------------------

func TestValidationGateRejectsBotChallengeServedAs200(t *testing.T) {
	body := `<html><head><title>Just a moment...</title></head><body>Checking your browser before accessing.</body></html>`
	v := Validate(Candidate{URL: "https://www.tesla.com/careers", Kind: KindCareerSite, Source: "nav_anchor"},
		htmlResponse("https://www.tesla.com/careers", body))
	if v.Accepted() {
		t.Fatalf("accepted a Cloudflare challenge page: %+v", v)
	}
	if v.Reason != "bot_challenge" {
		t.Errorf("reason = %q, want bot_challenge", v.Reason)
	}
}

func TestValidationGateRejectsParkedDomain(t *testing.T) {
	body := `<html><head><title>Coming Soon</title></head><body>This domain is for sale.</body></html>`
	v := Validate(Candidate{URL: "https://citibnqa.com/", Kind: KindWebsite},
		htmlResponse("https://citibnqa.com/", body))
	if v.Accepted() {
		t.Fatalf("accepted a parked domain: %+v", v)
	}
	if v.Reason != "parked_domain" {
		t.Errorf("reason = %q, want parked_domain", v.Reason)
	}
}

// --- kind-specific rules --------------------------------------------------

// A corporate homepage needs no careers evidence: it is the stepping stone, and the careers tier
// runs against it afterwards.
func TestValidationGateAcceptsPlainHomepage(t *testing.T) {
	v := Validate(Candidate{URL: "https://www.apple.com", Kind: KindWebsite, CompanyName: "Apple Inc."},
		okResponse("https://www.apple.com", "Apple", "<h1>Apple</h1>"))
	if !v.Accepted() {
		t.Fatalf("rejected a plain homepage: %+v", v)
	}
}

// A careers candidate that shows none of the careers signals is rejected even though it is a real,
// well-formed page.
func TestValidationGateRejectsPageWithNoCareersSignal(t *testing.T) {
	v := Validate(Candidate{URL: "https://example.com/about", Kind: KindCareerSite, Source: "path_heuristic"},
		okResponse("https://example.com/about", "About Example", "<h1>About us</h1><p>Our history.</p>"))
	if v.Accepted() {
		t.Fatalf("accepted a page with no careers signal: %+v", v)
	}
	if v.Reason != "no_careers_signal" {
		t.Errorf("reason = %q, want no_careers_signal", v.Reason)
	}
}

// "Open positions" in the body is enough, even when the title and path say nothing.
func TestValidationGateAcceptsOnOpeningsPhraseAlone(t *testing.T) {
	v := Validate(Candidate{URL: "https://example.com/x", Kind: KindCareerSite, Source: "path_heuristic"},
		okResponse("https://example.com/x", "Example", "<p>We have 12 open positions.</p>"))
	if !v.Accepted() {
		t.Fatalf("rejected a page advertising open positions: %+v", v)
	}
}

// A path heuristic that lands on an investor-relations section has found the wrong page.
func TestValidationGateRejectsInvestorPathFromPathHeuristic(t *testing.T) {
	v := Validate(Candidate{URL: "https://example.com/investors/jobs", Kind: KindCareerSite, Source: "path_heuristic"},
		okResponse("https://example.com/investors/jobs", "Investor Jobs", "<h1>Investor relations</h1>"))
	if v.Accepted() {
		t.Fatalf("accepted an investor-relations path: %+v", v)
	}
	if v.Reason != "product_or_investor_path" {
		t.Errorf("reason = %q, want product_or_investor_path", v.Reason)
	}
}

// The same path found by the company's own navigation is what the company publishes, so the origin
// changes the decision.
func TestValidationGateAllowsInvestorPathWhenTheCompanyLinksIt(t *testing.T) {
	v := Validate(Candidate{URL: "https://example.com/investors/careers", Kind: KindCareerSite,
		CompanyName: "Example", Source: "nav_anchor"},
		okResponse("https://example.com/investors/careers", "Careers at Example", "<h1>Careers</h1>"))
	if !v.Accepted() {
		t.Fatalf("rejected a company-navigated careers link: %+v", v)
	}
}

// --- redirects reveal the vendor -----------------------------------------

// The ATS fingerprint falls out of the redirect: Nike's branded host resolves to Workday.
func TestValidationGateRecordsFinalURLAfterRedirect(t *testing.T) {
	body := `<html><head><title>Nike Careers</title></head><body><h1>Careers</h1></body></html>`
	v := Validate(Candidate{URL: "https://jobs.nike.com/", Kind: KindCareerSite, CompanyName: "Nike", Source: "nav_anchor"},
		Response{FinalURL: "https://careers.nike.com/", Status: 200, ContentType: "text/html", Body: []byte(body)})
	if !v.Accepted() {
		t.Fatalf("rejected the Nike careers redirect: %+v", v)
	}
	if v.FinalURL != "https://careers.nike.com/" {
		t.Errorf("FinalURL = %q, want the redirect target", v.FinalURL)
	}
}

func TestValidationGateRejectsUnparseableFinalURL(t *testing.T) {
	v := Validate(Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		Response{FinalURL: "not a url", Status: 200, ContentType: "text/html", Body: []byte("<html>x</html>")})
	if v.Accepted() {
		t.Fatalf("accepted an unparseable final URL: %+v", v)
	}
	if v.Reason != "unparseable_final_url" {
		t.Errorf("reason = %q", v.Reason)
	}
}

// --- helpers --------------------------------------------------------------

func TestIsLocaleOnlyPath(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://example.com/en-us", true},
		{"https://example.com/en-us/", true},
		{"https://example.com/en", true},
		{"https://example.com/pt_BR", true},
		{"https://example.com/us/en/careers", false},
		{"https://example.com/careers", false},
		{"https://example.com/", false},
		{"https://example.com/aapl", false},
	} {
		if got := isLocaleOnlyPath(tc.url); got != tc.want {
			t.Errorf("isLocaleOnlyPath(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestSameSiteIgnoresWwwAndSubdomains(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://www.apple.com/careers", "https://apple.com/jobs", true},
		{"https://jobs.nike.com/", "https://www.nike.com/careers", true},
		{"https://www.apple.com/careers", "https://boards.greenhouse.io/apple", false},
	} {
		if got := sameSite(tc.a, tc.b); got != tc.want {
			t.Errorf("sameSite(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestDistinctiveTokenPicksTheLongestWord(t *testing.T) {
	for _, tc := range []struct{ company, want string }{
		{"JPMorgan Chase & Co.", "jpmorgan"},
		{"Apple Inc.", "apple"},
		{"3M", ""},
		{"AT&T", ""},
		{"The Coca-Cola Company", "coca"},
	} {
		if got := distinctiveToken(tc.company); got != tc.want {
			t.Errorf("distinctiveToken(%q) = %q, want %q", tc.company, got, tc.want)
		}
	}
}

func TestContainsCompanyTokenAcceptsAbbreviatedTitles(t *testing.T) {
	// The company is "JPMorgan Chase & Co." while the page says "JPMorganChase".
	if !containsCompanyToken("Careers | JPMorganChase", "JPMorgan Chase & Co.") {
		t.Error("did not match an abbreviated title to its company")
	}
	if containsCompanyToken("Jobs", "OpenAI") {
		t.Error("matched a generic title to a company")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
