// resolve.go: walk the resolution ladder for one company.
//
// Tiers 3 and 4 are wired here: robots.txt, sitemap, and the homepage anchor scan, each candidate
// passing through the validation gate before it is believed. Every attempt is recorded, accepted or
// not, so a wrong pick is diagnosable after the fact rather than mysterious.
//
// The network is behind Fetcher, so the whole ladder is exercised in tests against recorded
// responses and nothing here needs a socket.
//
// design: docs/sp1500-plan.md, sections 7.1 and 7.2.
package careers

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Fetcher retrieves a URL.
//
// The contract is spelled out here rather than left to the implementation because the recorded
// fixtures can only exercise what the seam exposes. A real Fetcher that returns (body, error) with
// the status embedded, against a stub that returns (body, finalURL, status), would pass every test
// and fail on first contact with a real site.
//
//   - Return (Response, nil) whenever an HTTP response was received, *including* 4xx and 5xx.
//     A 403 or 429 is an answer: the gate distinguishes "forbidden" and "rate_limited" from
//     "unreachable", and collapsing them into a Go error would lose that distinction and make a
//     blocked site look like a dead one.
//
//   - Return a non-nil error only when no complete response was received: DNS failure, connection
//     refused or reset, TLS failure, timeout, or too many redirects. The caller records that as
//     StatusError through Response.TransportError, which is an *unknown*, not a rejection, so the
//     site stays retryable.
//
//   - Redirects are followed, up to a bounded number of hops (5 is the intended default). The
//     returned FinalURL is the document actually read, not the URL requested. This is load-bearing:
//     jobs.nike.com resolving to careers.nike.com, or a branded page resolving to a Workday tenant,
//     is how the applicant-tracking vendor is identified at all.
//
//   - Status is the final response's status, never a redirect's 3xx.
//
//   - ContentType is the final response's Content-Type header including any parameters, because the
//     gate branches on it to choose the JSON rules over the HTML ones rather than sniffing bytes.
//
//   - Body is already bounded by the caller's configured limit. A vendor's job board can be tens of
//     megabytes (Ashby returned 13.8MB for one company during planning), so the limit belongs here
//     rather than in the gate.
//
//   - Body may therefore be TRUNCATED, and a consumer must not assume it is complete. Truncation is
//     not a failure as long as the decision does not depend on the missing part, which is why the
//     gate reads only from the front of the document: the <title> and <h1> are in the first
//     kilobytes, JSON-LD blocks sit in <head>, and an ATS board's entry count is established by the
//     opening of its array. A tier whose decision genuinely needs the whole body - a paginated job
//     list, say - must ask the fetcher for an unbounded or cursor-based read rather than silently
//     deciding on a cut-off document. What must never happen is a truncation being reported as a
//     content mismatch: that would look like "this is the wrong page" when the truth is "I stopped
//     reading early".
//
//   - The request carries the configured User-Agent and uses normal TLS verification. Neither is
//     spoofed or relaxed: 403 is recorded as a forbidden response, not disguised.
//
// A timeout is a per-request, configurable setting, and surfaces as a transport error wrapping
// context.DeadlineExceeded so an attempt record can say "timeout" rather than "network".
//
// maxBodyBytes bounds the response body. It is a parameter rather than a fetcher setting because
// the right bound is a property of the expected document, not of the client: a corporate homepage is
// a few hundred kilobytes, while an Ashby job board for one company measured 13.8MB during planning.
// A single global bound would either truncate the boards or make every HTML fetch malloc 24MB.
//
// The fetcher must still return a Response when the body is cut, with Truncated set. Deciding what
// truncation means belongs to the caller: the gate reads only the front of a document, whereas a
// JSON tier must treat a cut body as a hard rejection.
type Fetcher interface {
	Fetch(ctx context.Context, url string, maxBodyBytes int64) (Response, error)
}

// HTMLMaxBodyBytes bounds a response expected to be a web page. Comfortably above every homepage
// and careers page measured, and far below what a job-board API can return.
const HTMLMaxBodyBytes = 2 << 20

// Limits bound the work one company can cause. They exist because a resolver that follows every
// link on a large corporate site will not finish, and because the odd site publishes thousands of
// sitemap URLs.
type Limits struct {
	// MaxSitemaps caps how many sitemaps are fetched from a sitemap index.
	MaxSitemaps int
	// MaxCandidates caps how many validated candidates are fetched.
	MaxCandidates int
}

// DefaultLimits are the bounds used when Limits is zero.
func DefaultLimits() Limits { return Limits{MaxSitemaps: 5, MaxCandidates: 6} }

func (l Limits) withDefaults() Limits {
	if l.MaxSitemaps <= 0 {
		l.MaxSitemaps = 5
	}
	if l.MaxCandidates <= 0 {
		l.MaxCandidates = 6
	}
	return l
}

// Attempt is one candidate that was tried, mirroring url_resolution_attempts.
type Attempt struct {
	Source          string
	CandidateURL    string
	Kind            Kind
	HTTPStatus      int
	FinalURL        string
	Title           string
	ValidationState ValidationStatus
	RejectionReason Reason
	Evidence        string
	// Body is the response that produced the decision, kept so the caller can retain it as
	// evidence. The ladder does no I/O, so writing it is the caller's job - but the ladder is the
	// only place that has the bytes alongside the attempt they belong to.
	Body []byte
	// ContentType decides the evidence file's extension.
	ContentType string
}

// Outcome is what resolution concluded for one company.
type Outcome struct {
	// CareerSiteURL is empty when nothing validated. Empty is the correct answer for an
	// unresolved company: a wrong URL is worse than none, because it silently poisons everything
	// downstream that trusts it.
	CareerSiteURL string
	// Source names the tier that produced it.
	Source string
	// ATSHost is the host the winning URL finally resolved to, when that differs from the
	// candidate. A branded careers page usually redirects to the vendor that hosts it, and that
	// redirect is what identifies the applicant-tracking system.
	ATSHost string
	// FinalURL is where the winning candidate actually landed.
	FinalURL string
	// Title is the winning document's title, kept so a bad pick can be judged later.
	Title string
	// Attempts is everything that was tried, in order.
	Attempts []Attempt
}

// CareerSiteURLIsSet reports whether resolution found a validated careers URL. It exists so callers
// and tests do not have to distinguish "empty because unresolved" from "empty because not asked".
func (o Outcome) CareerSiteURLIsSet() bool { return o.CareerSiteURL != "" }

// Resolver walks the ladder for one company.
type Resolver struct {
	Fetcher Fetcher
	Limits  Limits
}

// Resolve finds a company's careers site starting from its homepage.
//
// homepageURL must be absolute. companyName is used for the title checks and may be empty, in which
// case third-party boards cannot be verified and are rejected rather than accepted.
func (r Resolver) Resolve(ctx context.Context, companyName, homepageURL string) (Outcome, error) {
	if r.Fetcher == nil {
		return Outcome{}, errors.New("careers: Resolver.Fetcher is nil")
	}
	limits := r.Limits.withDefaults()

	out := Outcome{}
	base, err := parseURL(homepageURL)
	if err != nil {
		return out, fmt.Errorf("careers: homepage %q: %w", homepageURL, err)
	}
	origin := base.Scheme + "://" + base.Host

	// Tier 3a: robots.txt. Cheap, official, and occasionally names the careers host outright.
	robotsCandidates := r.fromRobots(ctx, companyName, origin, &out)

	// Tier 4: the homepage anchor scan, the workhorse.
	anchorCandidates := r.fromHomepage(ctx, companyName, homepageURL, &out)

	// Tier 3b: sitemap. Last because it needs a second request and often lists thousands of URLs.
	sitemapCandidates := r.fromSitemaps(ctx, companyName, origin, robotsCandidates.sitemaps, limits, &out)

	// Try every candidate, best first, and stop at the first that validates. Candidate order is
	// the ranking the tiers produced: anchor scan by score, sitemap by path depth.
	// Anchors first (already ranked by the scan), then sitemap hits by path depth, then robots
	// paths. Deduplicated because the same URL commonly appears in both the navigation and the
	// sitemap, and a duplicate would spend one of the bounded fetches for nothing.
	ordered := dedupeCandidates(append(append(anchorCandidates.candidates, sitemapCandidates...), robotsCandidates.paths...))
	if len(ordered) > limits.MaxCandidates {
		ordered = ordered[:limits.MaxCandidates]
	}

	for _, cand := range ordered {
		resp, err := r.Fetcher.Fetch(ctx, cand.URL, HTMLMaxBodyBytes)
		if err != nil && resp.TransportError == nil {
			resp.TransportError = err
		}
		v := Validate(cand, resp)
		out.Attempts = append(out.Attempts, attemptFrom(cand, resp, v))
		if !v.Accepted() {
			continue
		}
		out.CareerSiteURL = v.FinalURL
		out.Source = cand.Source
		out.FinalURL = v.FinalURL
		out.Title = v.Title
		if host := hostOfURL(v.FinalURL); host != "" && !sameSite(v.FinalURL, homepageURL) {
			out.ATSHost = host
		}
		return out, nil
	}

	return out, nil
}

// --- tier 3a: robots.txt --------------------------------------------------

type robotsResult struct {
	sitemaps []SitemapRef
	paths    []Candidate
}

func (r Resolver) fromRobots(ctx context.Context, companyName, origin string, out *Outcome) robotsResult {
	robotsURL := origin + "/robots.txt"
	var res robotsResult

	resp, err := r.Fetcher.Fetch(ctx, robotsURL, HTMLMaxBodyBytes)
	if err != nil && resp.TransportError == nil {
		resp.TransportError = err
	}

	// robots.txt is not a careers proof, so it does not go through the gate: a missing file
	// (404) is normal and not a rejection worth recording as a failed candidate. Its contents
	// are the payload.
	if resp.Status != 200 || len(resp.Body) == 0 {
		return res
	}
	sitemaps, paths := ParseRobots(string(resp.Body))
	res.sitemaps = sitemaps
	for _, p := range paths {
		abs, err := resolveURL(origin+"/", p)
		if err != nil {
			continue
		}
		res.paths = append(res.paths, Candidate{
			URL: abs, Base: origin + "/", Kind: KindCareerSite,
			CompanyName: companyName, Source: "robots_path",
		})
	}
	// A sitemap found here is worth recording as an attempt even though it is not a careers URL:
	// the audit trail should show what the tier discovered.
	for _, s := range sitemaps {
		out.Attempts = append(out.Attempts, Attempt{
			Source: "robots_sitemap", CandidateURL: s.URL, Kind: KindCareerSite,
			ValidationState: StatusAccepted, Evidence: "sitemap_declared",
		})
	}
	return res
}

// --- tier 3b: sitemaps ----------------------------------------------------

func (r Resolver) fromSitemaps(ctx context.Context, companyName, origin string, refs []SitemapRef, limits Limits, out *Outcome) []Candidate {
	// Without a declared sitemap, try the conventional location once.
	if len(refs) == 0 {
		refs = []SitemapRef{{URL: origin + "/sitemap.xml", Source: "convention"}}
	}
	if len(refs) > limits.MaxSitemaps {
		refs = refs[:limits.MaxSitemaps]
	}

	var candidates []Candidate
	for _, ref := range refs {
		if !sameSite(ref.URL, origin+"/") {
			// A declared sitemap on another host is a legitimate pointer, so it is followed,
			// but only after the same-site ones.
		}
		resp, err := r.Fetcher.Fetch(ctx, ref.URL, HTMLMaxBodyBytes)
		if err != nil && resp.TransportError == nil {
			resp.TransportError = err
		}
		if resp.Status != 200 || len(resp.Body) == 0 {
			continue
		}
		locs, isIndex := ParseSitemap(string(resp.Body))
		if isIndex {
			// One level of nesting only: following an index of indexes is how a resolver ends up
			// fetching a hundred documents for one company.
			for i, loc := range locs {
				if i >= limits.MaxSitemaps {
					break
				}
				sub, err := r.Fetcher.Fetch(ctx, loc, HTMLMaxBodyBytes)
				if err != nil && sub.TransportError == nil {
					sub.TransportError = err
				}
				if sub.Status != 200 || len(sub.Body) == 0 {
					continue
				}
				subLocs, subIndex := ParseSitemap(string(sub.Body))
				if subIndex {
					continue
				}
				for _, u := range CareersURLsFromSitemap(subLocs) {
					candidates = append(candidates, Candidate{
						URL: u, Base: ref.URL, Kind: KindCareerSite,
						CompanyName: companyName, Source: "sitemap",
					})
				}
			}
			continue
		}
		for _, u := range CareersURLsFromSitemap(locs) {
			candidates = append(candidates, Candidate{
				URL: u, Base: ref.URL, Kind: KindCareerSite,
				CompanyName: companyName, Source: "sitemap",
			})
		}
	}
	return candidates
}

// --- tier 4: homepage anchors --------------------------------------------

type anchorResult struct {
	candidates []Candidate
}

func (r Resolver) fromHomepage(ctx context.Context, companyName, homepageURL string, out *Outcome) anchorResult {
	var res anchorResult

	resp, err := r.Fetcher.Fetch(ctx, homepageURL, HTMLMaxBodyBytes)
	if err != nil && resp.TransportError == nil {
		resp.TransportError = err
	}

	// The homepage is validated as a homepage, so a bot wall is recorded as such rather than
	// silently producing no candidates.
	home := Candidate{URL: homepageURL, Kind: KindWebsite, CompanyName: companyName, Source: "homepage"}
	v := Validate(home, resp)
	out.Attempts = append(out.Attempts, attemptFrom(home, resp, v))
	if !v.Accepted() {
		return res
	}

	for _, a := range Extract(v.FinalURL, string(resp.Body)) {
		// The kind follows where the URL points, not which tier found it. A link to a known
		// applicant-tracking host is a board, so it takes the stricter board rules - including
		// the requirement that the company be verifiable from the page.
		kind := KindCareerSite
		if isATSHost(hostOfURL(a.URL)) {
			kind = KindATSBoard
		}
		res.candidates = append(res.candidates, Candidate{
			URL: a.URL, Base: v.FinalURL, Kind: kind,
			CompanyName: companyName, Source: "nav_anchor",
		})
	}
	return res
}

// --- helpers --------------------------------------------------------------

func attemptFrom(c Candidate, r Response, v Verdict) Attempt {
	return Attempt{
		Source:          c.Source,
		CandidateURL:    c.URL,
		Kind:            c.Kind,
		HTTPStatus:      r.Status,
		FinalURL:        v.FinalURL,
		Title:           v.Title,
		ValidationState: v.Status,
		RejectionReason: v.Reason,
		Evidence:        v.Evidence,
		Body:            r.Body,
		ContentType:     r.ContentType,
	}
}

// atsHosts are the applicant-tracking hosts this pipeline can *read*: a link to one is a board
// wherever it was found, and a board is held to a stricter standard than a company's own page.
//
// SmartRecruiters is deliberately absent even though its boards exist, because there is no tier that
// can validate them: the public postings API returned byte-identical 200 responses for a real
// company and a nonexistent one, so a board there cannot be corroborated. Classifying it as a board
// would let an unvalidated vendor URL be recorded as a known platform.
var atsHosts = []string{
	"boards.greenhouse.io", "job-boards.greenhouse.io",
	"jobs.lever.co", "jobs.ashbyhq.com",
	"myworkdayjobs.com", "icims.com", "eightfold.ai", "oraclecloud.com",
	"successfactors.com", "taleo.net", "workable.com", "recruiting.paylocity.com",
}

// isATSHost reports whether a host belongs to a known applicant-tracking vendor. Matching is by
// suffix because vendors use per-tenant subdomains: acme.wd1.myworkdayjobs.com and
// careers.nike.com both land on a vendor.
func isATSHost(host string) bool {
	host = strings.ToLower(host)
	for _, vendor := range atsHosts {
		if host == vendor || strings.HasSuffix(host, "."+vendor) {
			return true
		}
	}
	return false
}

// dedupeCandidates removes repeats by URL, keeping the first occurrence, which is the highest-ranked
// one because each tier emits its candidates in order.
func dedupeCandidates(cands []Candidate) []Candidate {
	seen := make(map[string]struct{}, len(cands))
	out := cands[:0]
	for _, c := range cands {
		if _, dup := seen[c.URL]; dup {
			continue
		}
		seen[c.URL] = struct{}{}
		out = append(out, c)
	}
	return out
}
