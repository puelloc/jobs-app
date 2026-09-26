// reasons_test.go: the rejection-reason vocabulary is closed.
//
// Every Reason is persisted into url_resolution_attempts.rejection_reason, so an unused value is
// dead vocabulary and a produced-but-undeclared value is drift. These tests hold both ends: each
// declared value is exercised by a real code path, and no verdict produces a value outside the set.
package careers

import (
	"strconv"
	"strings"
	"testing"
)

// provocations maps each Reason to a candidate and response that provokes it. A Reason with no
// entry here is either unreachable or untested, and TestEveryReasonIsProducible fails on it.
var provocations = map[Reason]struct {
	candidate Candidate
	response  Response
}{
	OutcomeTransportError: {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{TransportError: errString("connection refused")},
	},
	OutcomeNoStatus: {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{FinalURL: "https://example.com/careers"},
	},
	OutcomeUnparseableFinalURL: {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{FinalURL: "not a url", Status: 200, ContentType: "text/html", Body: []byte("<html>x</html>")},
	},
	OutcomeForbidden: {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{FinalURL: "https://example.com/careers", Status: 403, ContentType: "text/html", Body: []byte("<html>x</html>")},
	},
	OutcomeRateLimited: {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{FinalURL: "https://example.com/careers", Status: 429, ContentType: "text/html", Body: []byte("<html>x</html>")},
	},
	OutcomeEmptyBody: {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{FinalURL: "https://example.com/careers", Status: 200, ContentType: "text/html", Body: []byte("  ")},
	},
	OutcomeUnknownKind: {
		candidate: Candidate{URL: "https://example.com/x", Kind: Kind("nonsense")},
		response:  Response{FinalURL: "https://example.com/x", Status: 200, ContentType: "text/html", Body: []byte("<html>x</html>")},
	},
	OutcomeBotChallenge: {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite, CompanyName: "Example"},
		response: htmlResponse("https://example.com/careers",
			`<html><head><title>Just a moment...</title></head><body>Checking your browser.</body></html>`),
	},
	OutcomeParkedDomain: {
		candidate: Candidate{URL: "https://example.com/", Kind: KindWebsite},
		response: htmlResponse("https://example.com/",
			`<html><head><title>Coming Soon</title></head><body>This domain is for sale.</body></html>`),
	},
	OutcomeGenericTitleWithoutCompany: {
		candidate: Candidate{URL: "https://jobs.ashbyhq.com/zzz", Kind: KindATSBoard, CompanyName: "Northwind Traders"},
		response:  htmlResponse("https://jobs.ashbyhq.com/zzz", fakeSlugAshby),
	},
	OutcomeUnverifiableATSTitle: {
		candidate: Candidate{URL: "https://jobs.ashbyhq.com/zzz", Kind: KindATSBoard},
		response:  htmlResponse("https://jobs.ashbyhq.com/zzz", fakeSlugAshby),
	},
	OutcomeProductOrInvestorPath: {
		candidate: Candidate{URL: "https://example.com/investors/jobs", Kind: KindCareerSite, Source: "path_heuristic"},
		response:  okResponse("https://example.com/investors/jobs", "Investor Jobs", "<h1>Investor relations</h1>"),
	},
	OutcomeLocaleOnlyPath: {
		candidate: Candidate{URL: "https://example.com/en-us", Kind: KindCareerSite, Source: "path_heuristic", CompanyName: "Example"},
		response:  okResponse("https://example.com/en-us", "Careers at Example", "<h1>Careers</h1><p>Search jobs</p>"),
	},
	OutcomeNoCareersSignal: {
		candidate: Candidate{URL: "https://example.com/about", Kind: KindCareerSite, Source: "path_heuristic"},
		response:  okResponse("https://example.com/about", "About Example", "<h1>About us</h1><p>Our history.</p>"),
	},
	OutcomeATSBoardEmpty: {
		candidate: Candidate{URL: "https://boards-api.greenhouse.io/v1/boards/x/jobs", Kind: KindATSBoard},
		response: Response{FinalURL: "https://boards-api.greenhouse.io/v1/boards/x/jobs", Status: 200,
			ContentType: "application/json", Body: []byte(`{"jobs":[]}`)},
	},
	OutcomeHTMLHomepage: {
		candidate: Candidate{URL: "https://www.apple.com", Kind: KindWebsite, CompanyName: "Apple Inc."},
		response:  okResponse("https://www.apple.com", "Apple", "<h1>Apple</h1>"),
	},
	OutcomeHTMLCareers: {
		candidate: Candidate{URL: "https://www.salesforce.com/company/careers/", Kind: KindCareerSite,
			CompanyName: "Salesforce", Source: "nav_anchor"},
		response: htmlResponse("https://www.salesforce.com/company/careers/", salesforceReal),
	},
	OutcomeATSBoard: {
		candidate: Candidate{URL: "https://boards-api.greenhouse.io/v1/boards/airbnb/jobs", Kind: KindATSBoard},
		response: Response{FinalURL: "https://boards-api.greenhouse.io/v1/boards/airbnb/jobs", Status: 200,
			ContentType: "application/json", Body: []byte(`{"jobs":[{"id":1}]}`)},
	},
	httpReason(404): {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{FinalURL: "https://example.com/careers", Status: 404, ContentType: "text/html", Body: []byte("<html>x</html>")},
	},
	httpReason(500): {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{FinalURL: "https://example.com/careers", Status: 500, ContentType: "text/html", Body: []byte("<html>x</html>")},
	},
	httpReason(503): {
		candidate: Candidate{URL: "https://example.com/careers", Kind: KindCareerSite},
		response:  Response{FinalURL: "https://example.com/careers", Status: 503, ContentType: "text/html", Body: []byte("<html>x</html>")},
	},
}

// Every declared Reason has a code path that produces it. This is what keeps the vocabulary from
// decaying into a list of aspirations: adding a constant without a test fails here.
func TestEveryReasonIsProducible(t *testing.T) {
	for _, want := range AllReasons() {
		prov, ok := provocations[want]
		if !ok {
			t.Errorf("reason %q is declared but no test provokes it; either add a provocation or remove the constant", want)
			continue
		}
		got := Validate(prov.candidate, prov.response)
		if got.Reason != want {
			t.Errorf("reason %q: provoking it produced %q instead", want, got.Reason)
		}
	}
}

// And the converse: nothing produces a value outside the declared set, so a stray fmt.Sprintf cannot
// introduce an undeclared reason.
func TestNoVerdictProducesAnUndeclaredReason(t *testing.T) {
	declared := map[Reason]bool{}
	for _, r := range AllReasons() {
		declared[r] = true
	}

	for want, prov := range provocations {
		got := Validate(prov.candidate, prov.response)
		if !declared[got.Reason] {
			t.Errorf("provoking %q produced undeclared reason %q", want, got.Reason)
		}
	}
}

// Provocations whose Reason is an HTTP status must agree with the status they used, so a change to
// the http_<code> spelling is caught here rather than silently splitting a query.
func TestHTTPReasonsEncodeTheirStatus(t *testing.T) {
	for _, want := range AllReasons() {
		if !strings.HasPrefix(string(want), "http_") {
			continue
		}
		prov, ok := provocations[want]
		if !ok {
			continue // already reported by TestEveryReasonIsProducible
		}
		expected := "http_" + strconv.Itoa(prov.response.Status)
		if string(want) != expected {
			t.Errorf("reason %q does not match its provoking status %d (want %q)", want, prov.response.Status, expected)
		}
	}
}

// Reasons are stable identifiers, not prose. A capital letter or a space would make the set harder
// to query and would drift the moment someone made it friendlier.
func TestReasonSpellingIsStable(t *testing.T) {
	for _, r := range AllReasons() {
		s := string(r)
		if s == "" {
			t.Error("an empty reason was declared")
			continue
		}
		if strings.ToLower(s) != s {
			t.Errorf("reason %q is not lowercase", s)
		}
		if strings.ContainsAny(s, " \t") {
			t.Errorf("reason %q contains whitespace", s)
		}
	}
}

// The corporate stop-word list is the fix for `distinctiveToken("The Coca-Cola Company")` returning
// "company", which any page matches. A list is only a fix if every entry is exercised: the next
// Wikipedia edit adds "XYZ Group", and without this test the same vacuous-token bug returns.
//
// Testing every entry means the fifth instance of the absent-input pattern is caught by an existing
// test rather than by a new bug report.
func TestEveryCorporateStopWordIsIgnored(t *testing.T) {
	if len(corporateStopWords) == 0 {
		t.Fatal("the stop-word set is empty; distinctiveToken would fall back to matching suffixes")
	}
	for word := range corporateStopWords {
		if word == "" {
			t.Error("an empty stop word was declared")
			continue
		}
		if got := distinctiveToken(word); got != "" {
			t.Errorf("distinctiveToken(%q) = %q, want empty: a name made only of %q has no distinctive word",
				word, got, word)
		}
		// The same word adjacent to a real company word must not win.
		if got := distinctiveToken("Northwind " + word); got != "northwind" {
			t.Errorf("distinctiveToken(%q) = %q, want northwind", "Northwind "+word, got)
		}
	}
}

// A real company name whose only long word is a stop word plus a short brand must not fall through
// to matching the stop word.
func TestDistinctiveTokenPrefersARealWordOverAStopWord(t *testing.T) {
	for _, tc := range []struct{ company, want string }{
		{"The Coca-Cola Company", "coca"},
		{"Apple Inc.", "apple"},
		{"Alphabet Inc.", "alphabet"},
		{"Berkshire Hathaway", "berkshire"},
		{"Northwind Trading Group Holdings", "northwind"},
		{"News Corp", "news"},
		{"Fox Corporation", "fox"},
	} {
		if got := distinctiveToken(tc.company); got != tc.want {
			t.Errorf("distinctiveToken(%q) = %q, want %q", tc.company, got, tc.want)
		}
	}
}

// Names with no distinctive word at all stay empty, which is what lets callers reject rather than
// accept on a vacuous match.
func TestDistinctiveTokenIsEmptyForNamesWithoutAWord(t *testing.T) {
	for _, name := range []string{"3M", "AT&T", "Inc.", "The Company", "", "&&&"} {
		if got := distinctiveToken(name); got != "" {
			t.Errorf("distinctiveToken(%q) = %q, want empty", name, got)
		}
	}
}
