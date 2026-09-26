// Package careers resolves a company to its careers site URL, and validates whatever it finds.
//
// This file is the validation gate. It is the load-bearing piece of the resolution ladder: a
// candidate URL is never accepted on HTTP status alone. Ashby and SmartRecruiters return 200 for
// companies that do not exist, so a status-only gate writes fabricated URLs into the database;
// measurement during planning found a status-only slug probe reporting "16 of 16 companies found"
// when none of them were.
//
// It is deliberately a pure function over a response that the caller has already fetched: no I/O,
// no network, no database. That keeps the decision rules testable against recorded fixtures and
// lets the same gate guard every tier, including the browser-use worker in M3.
//
// design: docs/sp1500-plan.md, section 7.2.
package careers

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind is the sort of URL being validated, which selects the acceptance rules.
type Kind string

const (
	// KindWebsite is a company's corporate homepage.
	KindWebsite Kind = "website"
	// KindCareerSite is a company careers page, self-hosted or branded.
	KindCareerSite Kind = "career_site"
	// KindATSBoard is a third-party applicant-tracking board.
	KindATSBoard Kind = "ats_board"
)

// ValidationStatus mirrors url_resolution_attempts.validation_status.
type ValidationStatus string

const (
	StatusAccepted ValidationStatus = "accepted"
	StatusRejected ValidationStatus = "rejected"
	StatusError    ValidationStatus = "error"
)

// Candidate is a URL discovered by some tier, before it has been fetched.
type Candidate struct {
	// URL is the absolute URL as discovered, which may be relative to be joined against Base.
	URL string
	// Base is the page the candidate was found on. A relative URL is resolved against it.
	Base string
	// Kind selects the acceptance rules.
	Kind Kind
	// CompanyName is the company the candidate is for. Used for the company-token checks. It may
	// be empty, in which case token checks are skipped rather than failed.
	CompanyName string
	// Source names the tier that produced it, for the audit trail: "nav_anchor", "robots_sitemap",
	// "path_heuristic", "wikidata", "ats_api", "browser_use".
	Source string
}

// Response is what the caller observed when it fetched a Candidate.
type Response struct {
	// FinalURL is the URL after redirects. Empty means the request never produced one.
	FinalURL string
	// Status is the HTTP status. Zero means the request failed before a status was received.
	Status int
	// ContentType is the Content-Type header, with any parameters.
	ContentType string
	// Body is the response body, already bounded by the caller.
	Body []byte
	// TransportError is non-nil when the request itself failed. Its presence makes the verdict
	// StatusError rather than StatusRejected: an unreachable site is unknown, not disproven, and
	// must not be recorded as a negative result.
	TransportError error
}

// Verdict is the gate's decision.
type Verdict struct {
	Status ValidationStatus
	// Reason is a short machine-readable label, stored in rejection_reason. It is stable so runs
	// can be compared; prose belongs in comments, not here.
	Reason string
	// FinalURL is the URL the response actually came from, after redirects. It is the value worth
	// storing: a branded careers subdomain often redirects to the ATS that hosts it, and that
	// redirect is what reveals the applicant-tracking vendor.
	FinalURL string
	// Title is the document title, recorded so a bad pick is diagnosable after the fact.
	Title string
	// Evidence holds extra parsed facts, such as the JSON-LD JobPosting count, for the audit trail.
	Evidence string
}

// Accepted reports whether the gate accepted the candidate.
func (v Verdict) Accepted() bool { return v.Status == StatusAccepted }

// Validate decides whether a fetched response is the thing the candidate claimed to be.
func Validate(c Candidate, r Response) Verdict {
	resolved := c.URL
	if c.Base != "" {
		if abs, err := resolveURL(c.Base, c.URL); err == nil {
			resolved = abs
		}
	}

	if r.TransportError != nil {
		return Verdict{
			Status:   StatusError,
			Reason:   "transport_error",
			FinalURL: r.FinalURL,
		}
	}
	if r.Status == 0 {
		return Verdict{Status: StatusError, Reason: "no_status", FinalURL: r.FinalURL}
	}

	final := r.FinalURL
	if final == "" {
		final = resolved
	}
	v := Verdict{FinalURL: final}

	// A redirect target that cannot be parsed is a red flag: the stored URL would be unusable.
	if _, err := parseURL(final); err != nil {
		v.Status = StatusRejected
		v.Reason = "unparseable_final_url"
		return v
	}

	if r.Status == 403 {
		v.Status = StatusRejected
		v.Reason = "forbidden"
		return v
	}
	if r.Status == 429 {
		v.Status = StatusRejected
		v.Reason = "rate_limited"
		return v
	}
	if r.Status < 200 || r.Status > 299 {
		v.Status = StatusRejected
		v.Reason = fmt.Sprintf("http_%d", r.Status)
		return v
	}

	if r.Status == 204 || len(strings.TrimSpace(string(r.Body))) == 0 {
		v.Status = StatusRejected
		v.Reason = "empty_body"
		return v
	}

	switch c.Kind {
	case KindWebsite:
		return validateHTML(v, c, r)
	case KindCareerSite, KindATSBoard:
		if isJSONContentType(r.ContentType) {
			return validateATSJSON(v, r)
		}
		return validateHTML(v, c, r)
	default:
		v.Status = StatusError
		v.Reason = "unknown_kind"
		return v
	}
}

// validateHTML applies the HTML acceptance rules.
func validateHTML(v Verdict, c Candidate, r Response) Verdict {
	body := string(r.Body)
	title := documentTitle(body)
	v.Title = title

	if looksLikeChallenge(body, title) {
		v.Status = StatusRejected
		v.Reason = "bot_challenge"
		return v
	}
	if isParkedDomain(body, title) {
		v.Status = StatusRejected
		v.Reason = "parked_domain"
		return v
	}

	// A generic title is only a rejection when the company is absent from it. "OpenAI Jobs" is a
	// real board; a bare "Jobs" is what Ashby serves for a slug that does not exist.
	if isGenericTitle(title) {
		// containsCompanyToken is permissive when the caller supplied no name, or when the name
		// has no distinctive word ("3M", "AT&T"). Neither case can corroborate a page, so on a
		// third-party board - where the title is the whole of the positive evidence - an
		// unverifiable title is its own rejection rather than a vacuous pass.
		if c.Kind == KindATSBoard && distinctiveToken(c.CompanyName) == "" {
			v.Status = StatusRejected
			v.Reason = "unverifiable_ats_title"
			return v
		}
		if !containsCompanyToken(title, c.CompanyName) {
			v.Status = StatusRejected
			v.Reason = "generic_title_without_company"
			return v
		}
	}

	if c.Kind == KindWebsite {
		v.Status = StatusAccepted
		v.Reason = "html_homepage"
		return v
	}

	// Hard rejections run before the evidence test. Evidence is generous by design - a path
	// containing "careers" counts on its own - so a decoy that happens to carry one of those
	// signals would be accepted before the specific rule against it was ever consulted. That is
	// exactly how trailhead.salesforce.com/career-path/ slipped through: its title, heading and
	// path all matched.
	if isProductOrInvestorPath(v.FinalURL, c.Source) {
		v.Status = StatusRejected
		v.Reason = "product_or_investor_path"
		return v
	}
	if isLocaleOnlyPath(v.FinalURL) {
		v.Status = StatusRejected
		v.Reason = "locale_only_path"
		return v
	}

	jobPostings := countJobPostings(body)
	evidence := []string{}
	if jobPostings > 0 {
		evidence = append(evidence, fmt.Sprintf("jobposting=%d", jobPostings))
	}
	if hasCareersPath(v.FinalURL) {
		evidence = append(evidence, "careers_path")
	}
	if hasCareersSignal(title) {
		evidence = append(evidence, "careers_title")
	}
	if hasCareersSignal(documentHeading(body)) {
		evidence = append(evidence, "careers_heading")
	}
	if hasOpeningsPhrase(body) {
		evidence = append(evidence, "openings_phrase")
	}
	v.Evidence = strings.Join(evidence, ",")

	if len(evidence) == 0 {
		v.Status = StatusRejected
		v.Reason = "no_careers_signal"
		return v
	}
	v.Status = StatusAccepted
	v.Reason = "html_careers"
	return v
}

// validateATSJSON applies the rules for an applicant-tracking board's JSON API.
//
// Each vendor names the array differently, and a valid board must be non-empty: the same endpoints
// answer a nonexistent company with an empty collection, or with a 200 and no jobs at all.
func validateATSJSON(v Verdict, r Response) Verdict {
	body := string(r.Body)
	count := countATSEntries(body)
	v.Evidence = fmt.Sprintf("ats_entries=%d", count)
	if count == 0 {
		v.Status = StatusRejected
		v.Reason = "ats_board_empty"
		return v
	}
	v.Status = StatusAccepted
	v.Reason = "ats_board"
	return v
}

// --- individual rules ----------------------------------------------------

var (
	tagStripper = regexp.MustCompile(`(?s)<[^>]*>`)
	titleRE     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	h1RE        = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	jobPosting  = regexp.MustCompile(`"@type"\s*:\s*"JobPosting"`)
	// Vendors differ: Greenhouse and Ashby use "jobs", SmartRecruiters and others use "content"
	// or "postings".
	atsArray = regexp.MustCompile(`"jobs"\s*:\s*\[|"postings"\s*:\s*\[|"content"\s*:\s*\[`)
)

// genericTitles are titles that name the vendor or say nothing, rather than naming the company.
// Observed on nonexistent companies: Ashby answers "Jobs", SmartRecruiters "SmartRecruiters Job
// Search".
var genericTitles = map[string]bool{
	"jobs":            true,
	"job":             true,
	"careers":         true,
	"job search":      true,
	"job search page": true,
	"positions":       true,
	"open positions":  true,
	"career search":   true,
}

// genericTitleMarkers catch vendor-branded titles that embed the vendor's own name, such as
// "SmartRecruiters Job Search". They are matched as substrings because the exact wording varies by
// vendor and page variant, and a generic title is only ever a rejection when the company is absent.
var genericTitleMarkers = []string{"job search"}

func isGenericTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	if genericTitles[lower] {
		return true
	}
	for _, marker := range genericTitleMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// challengeMarkers appear on bot walls that answer 200 with a challenge page.
var challengeMarkers = []string{
	"just a moment",
	"checking your browser",
	"attention required! | cloudflare",
	"enable javascript and cookies to continue",
}

// parkedMarkers appear on domain-parking pages, which answer 200 while containing nothing.
var parkedMarkers = []string{
	"this domain is for sale",
	"buy this domain",
	"domain for sale",
	"coming soon",
	"parked",
}

// careersTokens mark a URL path, title, or heading as careers-related.
var careersTokens = []string{"career", "jobs", "job", "join us", "join-us", "work with us", "employment", "opportunities", "hiring"}

// openingsPhrases are body-text evidence that a page actually lists roles.
var openingsPhrases = []string{
	"open positions", "job openings", "open roles", "current openings",
	"search jobs", "find a job", "view all jobs", "see all jobs", "explore careers",
}

func documentTitle(body string) string {
	m := titleRE.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return normaliseSpace(tagStripper.ReplaceAllString(m[1], ""))
}

func documentHeading(body string) string {
	m := h1RE.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return normaliseSpace(tagStripper.ReplaceAllString(m[1], ""))
}

func countJobPostings(body string) int { return len(jobPosting.FindAllString(body, -1)) }

func countATSEntries(body string) int {
	if !atsArray.MatchString(body) {
		return 0
	}
	// Count object openings between the first array marker and its close. A JSON parse would need
	// the vendor's exact shape; counting the id/"id" fields is enough to tell an empty board from
	// a populated one, which is the only distinction the gate needs.
	return len(regexp.MustCompile(`"id"\s*:`).FindAllString(body, -1))
}

func normaliseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func looksLikeChallenge(body, title string) bool {
	haystack := strings.ToLower(title + "\n" + firstN(body, 4096))
	for _, marker := range challengeMarkers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

func isParkedDomain(body, title string) bool {
	haystack := strings.ToLower(title + "\n" + firstN(body, 4096))
	for _, marker := range parkedMarkers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

// containsCompanyToken reports whether the company's distinctive words appear in the haystack.
//
// The comparison uses the longest word in the company name rather than the whole name, because
// titles abbreviate: the company is "JPMorgan Chase & Co." while the page says "Careers |
// JPMorganChase".
func containsCompanyToken(haystack, company string) bool {
	haystack = strings.ToLower(haystack)
	token := distinctiveToken(company)
	if token == "" {
		// With no company name there is nothing to check against. Callers that require the token
		// pass a name; this stays permissive rather than inventing a rejection.
		return true
	}
	return strings.Contains(haystack, token)
}

// corporateStopWords are common in company names but say nothing about which company it is. Without
// them the longest word of "The Coca-Cola Company" is "company", which would match almost any page.
var corporateStopWords = map[string]bool{
	"the": true, "and": true, "company": true, "companies": true, "corp": true, "corporation": true,
	"inc": true, "incorporated": true, "holdings": true, "group": true, "plc": true, "ltd": true,
	"limited": true, "international": true, "global": true, "industries": true, "technologies": true,
	"technology": true, "solutions": true, "systems": true, "services": true, "financial": true,
	"brands": true, "products": true, "enterprises": true, "partners": true, "trust": true, "fund": true,
}

// distinctiveToken returns the longest word of at least four characters in a company name that is
// not a corporate stop word, lowercased. It returns "" when the name has no such word, which is the
// case for names like "3M" and "AT&T" that are entirely punctuation, digits or initials.
func distinctiveToken(company string) string {
	best := ""
	for _, field := range strings.FieldsFunc(company, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9')
	}) {
		if len(field) < 4 {
			continue
		}
		lower := strings.ToLower(field)
		if corporateStopWords[lower] {
			continue
		}
		if len(field) > len(best) {
			best = field
		}
	}
	return strings.ToLower(best)
}

func hasCareersSignal(s string) bool {
	lower := strings.ToLower(s)
	for _, token := range careersTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func hasOpeningsPhrase(body string) bool {
	lower := strings.ToLower(body)
	for _, phrase := range openingsPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// hasCareersPath reports whether any path segment is careers-related.
func hasCareersPath(raw string) bool {
	u, err := parseURL(raw)
	if err != nil {
		return false
	}
	for _, segment := range strings.Split(strings.ToLower(u.Path), "/") {
		if segment == "" {
			continue
		}
		for _, token := range []string{"career", "job", "join", "employment", "opportunit", "hiring"} {
			if strings.Contains(segment, token) {
				return true
			}
		}
	}
	return false
}

// localeSegment matches a bare locale path segment such as "en", "en-us", "en_US", "pt-br".
var localeSegment = regexp.MustCompile(`^[a-z]{2}([-_][a-z]{2,4})?$`)

// isLocaleOnlyPath reports whether every path segment is a locale qualifier, i.e. the URL points at
// a regional landing page rather than the site.
func isLocaleOnlyPath(raw string) bool {
	u, err := parseURL(raw)
	if err != nil {
		return false
	}
	segments := []string{}
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segments = append(segments, strings.ToLower(s))
		}
	}
	if len(segments) == 0 {
		return false
	}
	for _, s := range segments {
		if !localeSegment.MatchString(s) {
			return false
		}
	}
	return true
}

// pathTokens are the path words that mark a page as something other than the careers page it
// claimed to be.
var pathTokens = []string{"investor", "ir", "newsroom", "press", "support", "help", "shop", "store"}

// isProductOrInvestorPath rejects a candidate that is a different kind of page: an investor or
// newsroom section, a storefront, or a known product host.
//
// Path tokens match as prefixes, so "/investors/jobs" is caught by "investor".
//
// The source matters for the path test: a heuristic that tried "/jobs" and landed on
// "/investors/jobs" has found the wrong page, whereas a company's own navigation labelling a link
// "Careers" is what the company publishes as its entry point, wherever it points.
//
// The host test is not source-conditional. A product host is a different site, so a navigation link
// to it is the company pointing at a product rather than at its hiring.
func isProductOrInvestorPath(raw, source string) bool {
	u, err := parseURL(raw)
	if err != nil {
		return false
	}
	if isProductHost(u.Hostname()) {
		return true
	}
	if source == "nav_anchor" {
		return false
	}
	for _, segment := range strings.Split(strings.ToLower(u.Path), "/") {
		for _, token := range pathTokens {
			if strings.HasPrefix(segment, token) {
				return true
			}
		}
	}
	return false
}

// productHosts are hosts that serve a company's *product* rather than its hiring. The Salesforce
// navigation offers trailhead.salesforce.com/career-path/, whose title, heading and path all read
// as careers; only the host gives it away.
//
// This is a seed list, not a taxonomy. It is here so the observed decoy is rejected for the right
// reason and so the next one is a one-line addition.
var productHosts = map[string]bool{
	"trailhead.salesforce.com": true,
	"authy.com":                true,
}

func isProductHost(host string) bool { return productHosts[strings.ToLower(host)] }

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func isJSONContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "application/json") || strings.Contains(ct, "+json")
}
