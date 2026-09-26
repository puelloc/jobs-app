// resolve_test.go: tests for the resolution ladder.
//
// Every response is served from a map, so the whole ladder runs without a socket. The fixtures are
// the page shapes recorded during planning.
package careers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubFetcher serves canned responses by URL and counts how often each is fetched.
type stubFetcher struct {
	pages map[string]Response
	calls map[string]int
	err   error
}

func newStubFetcher(pages map[string]Response) *stubFetcher {
	return &stubFetcher{pages: pages, calls: map[string]int{}}
}

func (s *stubFetcher) Fetch(_ context.Context, url string) (Response, error) {
	s.calls[url]++
	if s.err != nil {
		return Response{}, s.err
	}
	if r, ok := s.pages[url]; ok {
		if r.FinalURL == "" {
			r.FinalURL = url
		}
		return r, nil
	}
	return Response{FinalURL: url, Status: 404, ContentType: "text/html", Body: []byte("<html>not found</html>")}, nil
}

func htmlPage(url, title, body string) Response {
	return Response{
		FinalURL:    url,
		Status:      200,
		ContentType: "text/html; charset=utf-8",
		Body:        []byte("<html><head><title>" + title + "</title></head><body>" + body + "</body></html>"),
	}
}

const acmeHome = "https://www.acme.com/"

func acmePages() map[string]Response {
	return map[string]Response{
		acmeHome: htmlPage(acmeHome, "Acme Corporation",
			`<header><nav><a href="/about">About</a><a href="/careers">Careers</a></nav></header>`),
		"https://www.acme.com/robots.txt": {
			FinalURL: "https://www.acme.com/robots.txt", Status: 200, ContentType: "text/plain",
			Body: []byte("User-agent: *\nDisallow: /admin/\n"),
		},
		"https://www.acme.com/sitemap.xml": {
			FinalURL: "https://www.acme.com/sitemap.xml", Status: 200, ContentType: "application/xml",
			Body: []byte(`<urlset><url><loc>https://www.acme.com/careers</loc></url></urlset>`),
		},
		"https://www.acme.com/careers": htmlPage("https://www.acme.com/careers",
			"Careers at Acme", `<h1>Careers</h1><p>Search jobs</p>`),
	}
}

func resolveAcme(t *testing.T, pages map[string]Response) (Outcome, *stubFetcher) {
	t.Helper()
	stub := newStubFetcher(pages)
	r := Resolver{Fetcher: stub}
	out, err := r.Resolve(context.Background(), "Acme Corporation", acmeHome)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return out, stub
}

func TestResolveFindsCareersViaNavigation(t *testing.T) {
	out, _ := resolveAcme(t, acmePages())
	if out.CareerSiteURL != "https://www.acme.com/careers" {
		t.Fatalf("CareerSiteURL = %q, want the navigation link. Attempts: %+v", out.CareerSiteURL, out.Attempts)
	}
	if out.Source != "nav_anchor" {
		t.Errorf("Source = %q, want nav_anchor", out.Source)
	}
	if out.Title != "Careers at Acme" {
		t.Errorf("Title = %q", out.Title)
	}
	if out.ATSHost != "" {
		t.Errorf("ATSHost = %q, want empty for a same-site resolution", out.ATSHost)
	}
}

// Every attempt is recorded, accepted or not, so a wrong pick is diagnosable.
func TestResolveRecordsEveryAttempt(t *testing.T) {
	pages := acmePages()
	// A body-copy decoy that carries a careers signal but is not the careers page.
	pages[acmeHome] = htmlPage(acmeHome, "Acme Corporation",
		`<header><nav><a href="/about">About</a><a href="/careers">Careers</a></nav></header>`+
			`<main><a href="/blog/careers-in-tech">Careers in tech</a></main>`)

	out, _ := resolveAcme(t, pages)
	if len(out.Attempts) < 2 {
		t.Fatalf("recorded %d attempts, want at least the homepage and the candidates", len(out.Attempts))
	}
	var sawHomepage bool
	for _, a := range out.Attempts {
		if a.Source == "homepage" {
			sawHomepage = true
			if a.ValidationState != StatusAccepted {
				t.Errorf("homepage attempt state = %q, want accepted", a.ValidationState)
			}
		}
	}
	if !sawHomepage {
		t.Error("the homepage fetch was not recorded")
	}
}

// A sitemap-only careers URL is still found, and the source says which tier produced it.
func TestResolveFallsBackToSitemap(t *testing.T) {
	pages := acmePages()
	// Remove the navigation link so only the sitemap can succeed.
	pages[acmeHome] = htmlPage(acmeHome, "Acme Corporation", `<nav><a href="/about">About</a></nav>`)

	out, stub := resolveAcme(t, pages)
	if out.CareerSiteURL != "https://www.acme.com/careers" {
		t.Fatalf("CareerSiteURL = %q, want the sitemap URL. Attempts: %+v", out.CareerSiteURL, out.Attempts)
	}
	if out.Source != "sitemap" {
		t.Errorf("Source = %q, want sitemap", out.Source)
	}
	if stub.calls["https://www.acme.com/sitemap.xml"] != 1 {
		t.Errorf("sitemap fetched %d times, want 1", stub.calls["https://www.acme.com/sitemap.xml"])
	}
}

// A homepage behind a bot wall is recorded as such, and the ladder stops rather than guessing.
func TestResolveStopsWhenTheHomepageIsBlocked(t *testing.T) {
	pages := map[string]Response{
		acmeHome: {FinalURL: acmeHome, Status: 403, ContentType: "text/html", Body: []byte("forbidden")},
	}
	out, stub := resolveAcme(t, pages)
	if out.CareerSiteURL != "" {
		t.Fatalf("CareerSiteURL = %q, want empty when the homepage is blocked", out.CareerSiteURL)
	}
	if len(out.Attempts) == 0 {
		t.Fatal("no attempts recorded")
	}
	if out.Attempts[0].RejectionReason != "forbidden" {
		t.Errorf("first attempt reason = %q, want forbidden", out.Attempts[0].RejectionReason)
	}
	// No candidates should have been fetched from a page that was never read.
	for url := range stub.calls {
		if url != acmeHome && url != "https://www.acme.com/robots.txt" && url != "https://www.acme.com/sitemap.xml" {
			t.Errorf("fetched %s despite the homepage being blocked", url)
		}
	}
}

// An unresolved company yields an empty URL, not a guess. A wrong URL is worse than none.
func TestResolveLeavesUnresolvedCompanyEmpty(t *testing.T) {
	pages := map[string]Response{
		acmeHome: htmlPage(acmeHome, "Acme Corporation", `<nav><a href="/about">About</a></nav>`),
		"https://www.acme.com/robots.txt": {
			FinalURL: "https://www.acme.com/robots.txt", Status: 404, ContentType: "text/plain", Body: nil,
		},
	}
	out, _ := resolveAcme(t, pages)
	if out.CareerSiteURL != "" {
		t.Fatalf("CareerSiteURL = %q, want empty", out.CareerSiteURL)
	}
	if out.Source != "" {
		t.Errorf("Source = %q, want empty", out.Source)
	}
}

// A branded careers link that redirects to a vendor reveals the vendor through the final host.
func TestResolveRecordsVendorHostFromRedirect(t *testing.T) {
	pages := map[string]Response{
		acmeHome:                           htmlPage(acmeHome, "Acme Corporation", `<nav><a href="https://jobs.acme.com/">Careers</a></nav>`),
		"https://www.acme.com/robots.txt":  {FinalURL: "https://www.acme.com/robots.txt", Status: 404, ContentType: "text/plain"},
		"https://www.acme.com/sitemap.xml": {FinalURL: "https://www.acme.com/sitemap.xml", Status: 404, ContentType: "application/xml"},
		"https://jobs.acme.com/": {
			// The redirect target is where the applicant-tracking system lives.
			FinalURL:    "https://acme.wd1.myworkdayjobs.com/AcmeCareers",
			Status:      200,
			ContentType: "text/html",
			Body:        []byte("<html><head><title>Acme Careers</title></head><body><h1>Careers</h1></body></html>"),
		},
	}
	out, _ := resolveAcme(t, pages)
	if !out.CareerSiteURLIsSet() {
		t.Fatalf("not resolved. Attempts: %+v", out.Attempts)
	}
	if out.FinalURL != "https://acme.wd1.myworkdayjobs.com/AcmeCareers" {
		t.Errorf("FinalURL = %q, want the redirect target", out.FinalURL)
	}
	if out.ATSHost != "acme.wd1.myworkdayjobs.com" {
		t.Errorf("ATSHost = %q, want the vendor host", out.ATSHost)
	}
}

// A third-party board with no company name to check against cannot be verified, so it is rejected
// rather than accepted on a vacuous match.
func TestResolveRejectsVendorBoardWithoutACompanyName(t *testing.T) {
	pages := map[string]Response{
		acmeHome:                           htmlPage(acmeHome, "Acme", `<nav><a href="https://jobs.ashbyhq.com/acme">Careers</a></nav>`),
		"https://www.acme.com/robots.txt":  {FinalURL: "https://www.acme.com/robots.txt", Status: 404, ContentType: "text/plain"},
		"https://www.acme.com/sitemap.xml": {FinalURL: "https://www.acme.com/sitemap.xml", Status: 404, ContentType: "application/xml"},
		"https://jobs.ashbyhq.com/acme": {
			FinalURL: "https://jobs.ashbyhq.com/acme", Status: 200, ContentType: "text/html",
			Body: []byte("<html><head><title>Jobs</title></head><body></body></html>"),
		},
	}
	stub := newStubFetcher(pages)
	// An empty company name: nothing to verify the board's title against.
	out, err := (Resolver{Fetcher: stub}).Resolve(context.Background(), "", acmeHome)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.CareerSiteURL != "" {
		t.Fatalf("resolved %q without a company name to verify against", out.CareerSiteURL)
	}
}

// A transport failure on the homepage is an error outcome, not a rejection that stops a retry.
func TestResolveRecordsTransportFailureAsError(t *testing.T) {
	stub := newStubFetcher(nil)
	stub.err = errors.New("connection refused")
	out, err := (Resolver{Fetcher: stub}).Resolve(context.Background(), "Acme", acmeHome)
	if err != nil {
		t.Fatalf("Resolve returned an error for a transport failure: %v", err)
	}
	if out.CareerSiteURL != "" {
		t.Errorf("CareerSiteURL = %q, want empty", out.CareerSiteURL)
	}
	if len(out.Attempts) == 0 || out.Attempts[0].ValidationState != StatusError {
		t.Errorf("first attempt = %+v, want an error state", out.Attempts)
	}
}

// A company with no homepage is a caller error, not an empty resolution.
func TestResolveRejectsAMissingHomepage(t *testing.T) {
	if _, err := (Resolver{Fetcher: newStubFetcher(nil)}).Resolve(context.Background(), "Acme", "not a url"); err == nil {
		t.Error("Resolve accepted an unparseable homepage, want an error")
	}
}

func TestResolveRequiresAFetcher(t *testing.T) {
	if _, err := (Resolver{}).Resolve(context.Background(), "Acme", acmeHome); err == nil {
		t.Error("Resolve ran with no fetcher, want an error")
	}
}

// The candidate budget bounds the work one company can cause.
func TestResolveRespectsTheCandidateLimit(t *testing.T) {
	var links strings.Builder
	links.WriteString(`<nav>`)
	for i := 0; i < 40; i++ {
		links.WriteString(`<a href="/careers/team-` + string(rune('a'+i%26)) + string(rune('0'+i/26)) + `">Careers</a>`)
	}
	links.WriteString(`</nav>`)

	pages := map[string]Response{
		acmeHome:                           htmlPage(acmeHome, "Acme", links.String()),
		"https://www.acme.com/robots.txt":  {FinalURL: "https://www.acme.com/robots.txt", Status: 404, ContentType: "text/plain"},
		"https://www.acme.com/sitemap.xml": {FinalURL: "https://www.acme.com/sitemap.xml", Status: 404, ContentType: "application/xml"},
	}
	stub := newStubFetcher(pages)
	r := Resolver{Fetcher: stub, Limits: Limits{MaxSitemaps: 1, MaxCandidates: 3}}
	if _, err := r.Resolve(context.Background(), "Acme", acmeHome); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	candidateFetches := 0
	for url, n := range stub.calls {
		if strings.Contains(url, "/careers/team-") {
			candidateFetches += n
		}
	}
	if candidateFetches > 3 {
		t.Errorf("fetched %d candidates, want at most 3", candidateFetches)
	}
}

func TestDedupeCandidatesKeepsTheFirstOccurrence(t *testing.T) {
	in := []Candidate{
		{URL: "https://a/1", Source: "nav_anchor"},
		{URL: "https://a/2", Source: "nav_anchor"},
		{URL: "https://a/1", Source: "sitemap"},
	}
	got := dedupeCandidates(in)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Source != "nav_anchor" {
		t.Errorf("kept %q, want the first occurrence", got[0].Source)
	}
}
