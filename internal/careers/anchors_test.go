// anchors_test.go: tests for the careers-link anchor scan.
//
// The pages are reduced from the real navigation recorded during planning (docs/sp1500-plan.md
// 4.11). No test here touches the network.
package careers

import (
	"strings"
	"testing"
)

// costcoNav reproduces the shape that produced costco.com/jobs.html, including the noisy "Join Us"
// membership link Nike's navigation also carries.
const costcoNav = `<html><head><title>Costco Wholesale</title></head><body>
<header><nav>
  <a href="/">Home</a>
  <a href="/membership">Join Us</a>
  <a href="/jobs.html">Careers</a>
  <a href="/shop">Shop</a>
</nav></header>
<main><p>Welcome to Costco.</p></main>
</body></html>`

// salesforceNav carries the real decoy: a Trailhead product page labelled "Career Paths" competing
// with the real careers link.
const salesforceNav = `<html><head><title>Salesforce</title></head><body>
<nav>
  <a href="https://trailhead.salesforce.com/career-path/">Career Paths</a>
  <a href="/company/careers/">See all careers</a>
</nav>
</body></html>`

// lockheedNav has the decoy that started the href="#" rule: the visible "Careers" label points
// nowhere, and the real link is elsewhere in the same nav.
const lockheedNav = `<html><head><title>Lockheed Martin</title></head><body>
<nav>
  <a href="#">Careers</a>
  <a href="/en-us/careers/index.html">Careers Home</a>
  <a href="https://lockheedmartin.eightfold.ai/careers">Search Open Jobs</a>
</nav>
</body></html>`

// nikeNav has a branded careers host, which is external to www.nike.com and must be kept.
const nikeNav = `<html><head><title>Nike. Just Do It.</title></head><body>
<nav>
  <a href="https://jobs.nike.com/">Careers</a>
  <a href="/membership">Join Us</a>
</nav>
</body></html>`

// bodyOnlyCareers puts the careers link in body copy rather than navigation, which is weaker
// evidence and must not outrank a navigation link.
const bodyOnlyCareers = `<html><body>
<nav><a href="/about">About</a></nav>
<main><p>Read our <a href="/blog/careers-in-tech">careers in tech</a> post.</p>
<p>Or see <a href="/jobs">Jobs</a>.</p></main>
</body></html>`

func topCandidate(t *testing.T, got []AnchorCandidate) AnchorCandidate {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("no candidates found")
	}
	return got[0]
}

func containsURL(cands []AnchorCandidate, url string) bool {
	for _, c := range cands {
		if c.URL == url {
			return true
		}
	}
	return false
}

func urlsOf(cands []AnchorCandidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.URL)
	}
	return out
}

func TestExtractFindsTheCostcoCareersLink(t *testing.T) {
	got := Extract("https://www.costco.com/", costcoNav)
	top := topCandidate(t, got)
	if top.URL != "https://www.costco.com/jobs.html" {
		t.Errorf("top candidate = %q, want the careers link. All: %v", top.URL, urlsOf(got))
	}
	if top.Text != "Careers" {
		t.Errorf("text = %q", top.Text)
	}
}

// "Join Us" is a membership link on Costco and Nike alike. It must not be mistaken for a careers
// link just because it contains "join".
func TestExtractDoesNotRankJoinUsAboveCareers(t *testing.T) {
	got := Extract("https://www.costco.com/", costcoNav)
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	if strings.Contains(got[0].URL, "membership") {
		t.Errorf("ranked the membership link first: %v", urlsOf(got))
	}
}

// The exact careers link outranks a product page that also contains a careers word.
func TestExtractRanksRealCareersAboveTheTrailheadDecoy(t *testing.T) {
	got := Extract("https://www.salesforce.com/", salesforceNav)
	top := topCandidate(t, got)
	if top.URL != "https://www.salesforce.com/company/careers/" {
		t.Errorf("top candidate = %q, want the real careers page. All: %v", top.URL, urlsOf(got))
	}
	// The decoy is still returned: the gate, not the scan, decides it is wrong.
	if !containsURL(got, "https://trailhead.salesforce.com/career-path/") {
		t.Error("the decoy was dropped entirely; the audit trail should record it")
	}
}

// The real Salesforce link text is "See all careers", which is not an exact label but is a clear
// signal.
func TestExtractScoresSeeAllCareersAsASignal(t *testing.T) {
	got := Extract("https://www.salesforce.com/", salesforceNav)
	for _, c := range got {
		if c.URL == "https://www.salesforce.com/company/careers/" {
			if c.Score < scoreTextSignal {
				t.Errorf("score = %d, want at least %d for 'See all careers'", c.Score, scoreTextSignal)
			}
			return
		}
	}
	t.Fatal("the real careers link is missing")
}

// An href="#" carries no destination and must not become a candidate.
func TestExtractSkipsTheHrefHashDecoy(t *testing.T) {
	got := Extract("https://www.lockheedmartin.com/", lockheedNav)
	for _, c := range got {
		if strings.HasSuffix(c.URL, "lockheedmartin.com/") {
			t.Errorf("the bare homepage was produced from an href=\"#\" near a Careers label: %v", urlsOf(got))
		}
	}
	top := topCandidate(t, got)
	if top.URL != "https://www.lockheedmartin.com/en-us/careers/index.html" {
		t.Errorf("top candidate = %q, want the real link. All: %v", top.URL, urlsOf(got))
	}
}

// A branded careers subdomain is first-party: jobs.nike.com and www.nike.com share a registrable
// domain, and the plan classifies a branded subdomain as first-party for career_site_url purposes.
func TestExtractTreatsBrandedSubdomainAsFirstParty(t *testing.T) {
	got := Extract("https://www.nike.com/", nikeNav)
	var found *AnchorCandidate
	for i := range got {
		if got[i].URL == "https://jobs.nike.com/" {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the jobs.nike.com link is missing: %v", urlsOf(got))
	}
	if found.External {
		t.Error("External = true for a branded subdomain of the same site")
	}
}

// A genuinely different registrable domain, such as an applicant-tracking vendor, is external.
func TestExtractFlagsAVendorDomainAsExternal(t *testing.T) {
	page := `<html><body><nav>
	<a href="https://boards.greenhouse.io/acme">Careers</a>
	</nav></body></html>`
	got := Extract("https://www.acme.com/", page)
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	if !got[0].External {
		t.Error("External = false for a vendor domain")
	}
}

// A paragraph about careers in tech is not the careers page.
func TestExtractPrefersNavigationLinkOverBodyCopy(t *testing.T) {
	got := Extract("https://example.com/", bodyOnlyCareers)
	top := topCandidate(t, got)
	if top.URL != "https://example.com/jobs" {
		t.Errorf("top candidate = %q, want /jobs over the blog post. All: %v", top.URL, urlsOf(got))
	}
}

// A link carrying no careers signal at all is noise and must be dropped.
func TestExtractDropsUnrelatedLinks(t *testing.T) {
	page := `<html><body><nav>
	<a href="/about">About us</a>
	<a href="/products">Products</a>
	<a href="mailto:hello@example.com">Email us</a>
	<a href="javascript:void(0)">Menu</a>
	</nav></body></html>`
	if got := Extract("https://example.com/", page); len(got) != 0 {
		t.Errorf("found %v in a page with no careers link", urlsOf(got))
	}
}

// The same link in header and footer is one candidate, keeping the higher score.
func TestExtractDeduplicatesRepeatedLinks(t *testing.T) {
	page := `<html><body>
	<header><nav><a href="/careers">Careers</a></nav></header>
	<footer><a href="/careers">Careers</a></footer>
	</body></html>`
	got := Extract("https://example.com/", page)
	if len(got) != 1 {
		t.Fatalf("got %d candidates for one link: %v", len(got), urlsOf(got))
	}
	if got[0].Score < scoreTextExact {
		t.Errorf("score = %d, want the exact-label score", got[0].Score)
	}
}

func TestExtractResolvesRelativeHrefs(t *testing.T) {
	page := `<html><body><nav><a href="careers/jobs">Careers</a></nav></body></html>`
	got := Extract("https://example.com/company/", page)
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	if got[0].URL != "https://example.com/company/careers/jobs" {
		t.Errorf("URL = %q", got[0].URL)
	}
}

func TestExtractStripsFragmentFromCandidate(t *testing.T) {
	page := `<html><body><nav><a href="/careers#open-roles">Careers</a></nav></body></html>`
	got := Extract("https://example.com/", page)
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	if got[0].URL != "https://example.com/careers" {
		t.Errorf("URL = %q, want the fragment removed", got[0].URL)
	}
}

// Single-quoted and unquoted hrefs both occur in the wild.
func TestExtractHandlesQuoteStyles(t *testing.T) {
	page := `<html><body><nav><a href='/careers'>Careers</a></nav></body></html>`
	if got := Extract("https://example.com/", page); len(got) == 0 || got[0].URL != "https://example.com/careers" {
		t.Errorf("single-quoted href not handled: %v", urlsOf(got))
	}
	page = `<html><body><nav><a href=/careers>Careers</a></nav></body></html>`
	if got := Extract("https://example.com/", page); len(got) == 0 || got[0].URL != "https://example.com/careers" {
		t.Errorf("unquoted href not handled: %v", urlsOf(got))
	}
}

// Links buried in a long body must not outrank a short navigation label.
func TestExtractScoresExactLabelAbovePathOnlySignal(t *testing.T) {
	page := `<html><body>
	<main><a href="/legal/careers-policy">Read our careers policy</a></main>
	<header><nav><a href="/join">Careers</a></nav></header>
	</body></html>`
	got := Extract("https://example.com/", page)
	if len(got) < 2 {
		t.Fatalf("expected both links, got %v", urlsOf(got))
	}
	if got[0].URL != "https://example.com/join" {
		t.Errorf("top candidate = %q, want the navigation label. All: %v", got[0].URL, urlsOf(got))
	}
}
