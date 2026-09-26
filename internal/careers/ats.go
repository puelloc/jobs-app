// ats.go: tier 5a - applicant-tracking boards read from their public JSON APIs.
//
// Only the vendors with a real public API are here: Greenhouse, Lever and Ashby. SmartRecruiters is
// deliberately absent. Its postings API returned byte-identical 200 responses for a real company and
// a nonexistent one during planning, so it cannot corroborate anything, and accepting an empty board
// would write a fabricated career_site_url - which the NULL-beats-fabricated decision forbids.
//
// Oracle Cloud, Eightfold, Workday, Phenom, SuccessFactors and Taleo are also absent: they have no
// public job-board API at all, and reading them means HTML extraction, which is M2b.
//
// design: docs/sp1500-plan.md, sections 4.12, 6 and 7.1 tier 5a.
package careers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ATS is a vendor whose JSON API this tier can read.
type ATS string

const (
	ATSGreenhouse ATS = "greenhouse"
	ATSLever      ATS = "lever"
	ATSAshby      ATS = "ashby"
)

// Body bounds. Ashby returned 13.8MB for one company during planning, so its bound is three times
// the others'; a shared bound would either truncate its boards or make every Greenhouse call ready
// to buffer 24MB.
const (
	greenhouseMaxBodyBytes = 8 << 20
	leverMaxBodyBytes      = 8 << 20
	ashbyMaxBodyBytes      = 24 << 20
)

// ATSOutcome is the outcome of fetching one board.
type ATSOutcome struct {
	// Accepted reports whether a populated board was found.
	Accepted bool
	// Reason explains a rejection. Empty on acceptance.
	Reason Reason
	// TenantURL is the human-facing board URL, which is what gets stored as career_site_url. Never
	// the API URL.
	TenantURL string
	// APIURL is the endpoint that was queried, recorded for the audit trail.
	APIURL string
	// JobCount is the number of postings found, zero for a rejection.
	JobCount int
	// HTTPStatus is the final status of the API call.
	HTTPStatus int
	// Evidence summarises what was parsed, for the audit trail.
	Evidence string
}

// ATSResolver fetches and validates one vendor's board.
type ATSResolver struct {
	Fetcher Fetcher
	// Client is the vendor-neutral HTTP client seam. Empty fields fall back to the defaults.
	Client Client
}

// Client mirrors the homepage package's API roots so a test can point this tier at a local server.
type Client struct {
	Greenhouse string
	Lever      string
	Ashby      string
}

const (
	greenhouseEndpoint = "https://boards-api.greenhouse.io"
	leverEndpoint      = "https://api.lever.co"
	ashbyEndpoint      = "https://api.ashbyhq.com"
)

// Resolve identifies the vendor behind a tenant URL and fetches its board.
//
// tenantURL is the host-and-path the fingerprint tier found, such as https://jobs.ashbyhq.com/acme.
// A URL belonging to no known vendor is not an error: it is a board this tier cannot read, which is
// what OutcomeUnknownKind records.
func (r ATSResolver) Resolve(ctx context.Context, tenantURL string) (ATSOutcome, error) {
	if r.Fetcher == nil {
		return ATSOutcome{}, fmt.Errorf("careers: ATSResolver.Fetcher is nil")
	}

	tenant, ats, err := ParseTenant(tenantURL)
	if err != nil {
		return ATSOutcome{TenantURL: tenantURL, Reason: OutcomeUnknownKind}, nil
	}

	apiURL, bound := r.endpoint(ats, tenant)
	out := ATSOutcome{TenantURL: tenantURL, APIURL: apiURL}

	resp, err := r.Fetcher.Fetch(ctx, apiURL, bound)
	if err != nil && resp.TransportError == nil {
		resp.TransportError = err
	}
	out.HTTPStatus = resp.Status

	// The order of these checks is the point of the tier, so it is stated rather than implied:
	//
	//   1. truncation, because a cut body is also invalid JSON and would otherwise be reported as a
	//      malformed response. The two mean different things - an oversized board versus a broken
	//      one - and they suggest different fixes.
	//   2. transport failure, which is an unknown rather than a rejection.
	//   3. HTTP status.
	//   4. parse.
	//   5. emptiness.
	if resp.Truncated {
		out.Reason = OutcomeATSTruncatedBody
		return out, nil
	}
	if resp.TransportError != nil {
		if resp.TimedOut || isTimeout(resp.TransportError) {
			out.Reason = OutcomeTimeout
		} else {
			out.Reason = OutcomeTransportError
		}
		return out, nil
	}
	switch {
	case resp.Status == 403:
		out.Reason = OutcomeForbidden
		return out, nil
	case resp.Status == 429:
		out.Reason = OutcomeRateLimited
		return out, nil
	case resp.Status < 200 || resp.Status > 299:
		if resp.Status >= 500 {
			out.Reason = OutcomeATSHttp5xx
		} else {
			out.Reason = OutcomeATSHttp4xx
		}
		return out, nil
	}
	if len(strings.TrimSpace(string(resp.Body))) == 0 {
		out.Reason = OutcomeEmptyBody
		return out, nil
	}

	count, parseErr := CountBoardEntries(ats, resp.Body)
	if parseErr != nil {
		out.Reason = OutcomeATSInvalidJSON
		return out, nil
	}
	out.JobCount = count
	out.Evidence = fmt.Sprintf("ats_entries=%d vendor=%s", count, ats)
	if count == 0 {
		// A reachable board that lists nothing. This is the shape a nonexistent company produces,
		// which is why an empty board can never be accepted.
		out.Reason = OutcomeATSEmptyBoard
		return out, nil
	}

	out.Accepted = true
	return out, nil
}

// endpoint returns the API URL for a vendor and tenant slug, and the body bound to use.
func (r ATSResolver) endpoint(ats ATS, tenant string) (string, int64) {
	switch ats {
	case ATSGreenhouse:
		root := r.Client.Greenhouse
		if root == "" {
			root = greenhouseEndpoint
		}
		return fmt.Sprintf("%s/v1/boards/%s/jobs", root, url.PathEscape(tenant)), greenhouseMaxBodyBytes
	case ATSLever:
		root := r.Client.Lever
		if root == "" {
			root = leverEndpoint
		}
		return fmt.Sprintf("%s/v0/postings/%s?mode=json", root, url.PathEscape(tenant)), leverMaxBodyBytes
	case ATSAshby:
		root := r.Client.Ashby
		if root == "" {
			root = ashbyEndpoint
		}
		return fmt.Sprintf("%s/posting-api/job-board/%s", root, url.PathEscape(tenant)), ashbyMaxBodyBytes
	default:
		return "", 0
	}
}

// tenantHosts maps a vendor's hosts to the vendor. A tenant URL is matched by suffix because vendors
// use per-tenant subdomains: acme.wd1.myworkdayjobs.com belongs to Workday.
var tenantHosts = []struct {
	suffix string
	ats    ATS
}{
	{"boards.greenhouse.io", ATSGreenhouse},
	{"job-boards.greenhouse.io", ATSGreenhouse},
	{"jobs.lever.co", ATSLever},
	{"jobs.ashbyhq.com", ATSAshby},
}

// ParseTenant extracts the vendor and the slug from a board URL.
//
// The slug is the first path segment, which is the convention all three vendors use:
// https://jobs.ashbyhq.com/acme/careers and https://jobs.ashbyhq.com/acme both mean the tenant acme.
func ParseTenant(tenantURL string) (tenant string, ats ATS, err error) {
	u, err := parseURL(tenantURL)
	if err != nil {
		return "", "", err
	}
	host := strings.ToLower(u.Hostname())
	for _, entry := range tenantHosts {
		// Either the host is the vendor's own domain, or it is a per-tenant subdomain of it.
		if host != entry.suffix && !strings.HasSuffix(host, "."+entry.suffix) {
			continue
		}
		// The slug is the first path segment, or the subdomain when the path carries none:
		// jobs.ashbyhq.com/acme and acme.jobs.ashbyhq.com both mean the tenant acme.
		if slug := firstPathSegment(u.Path); slug != "" {
			return slug, entry.ats, nil
		}
		if slug := subdomainBefore(host, entry.suffix); slug != "" {
			return slug, entry.ats, nil
		}
		return "", "", fmt.Errorf("no tenant slug in %q", tenantURL)
	}
	return "", "", fmt.Errorf("no known vendor for host %q", host)
}

// subdomainBefore returns the host label immediately preceding a vendor suffix.
func subdomainBefore(host, suffix string) string {
	trimmed := strings.TrimSuffix(host, "."+suffix)
	if trimmed == host || trimmed == "" {
		return ""
	}
	labels := strings.Split(trimmed, ".")
	return labels[len(labels)-1]
}

func firstPathSegment(path string) string {
	for _, segment := range strings.Split(path, "/") {
		if segment != "" {
			return segment
		}
	}
	return ""
}

// CountBoardEntries parses a board and reports how many postings it lists.
//
// The three vendors agree on a "jobs" array and disagree on little else, so one shape covers them:
// the array is located and its length counted without decoding each posting, which matters when a
// board carries tens of megabytes of descriptions this tier does not need.
//
// The return is (0, nil) for a well-formed board with no postings, and a non-nil error only for a
// body that is not valid JSON at all. Those two cases must stay distinguishable: one is an empty
// board and the other is a broken response.
func CountBoardEntries(ats ATS, body []byte) (int, error) {
	// Lever returns a bare array, and decoding an array into the envelope struct fails outright -
	// so the array shape has to be recognised first, not fallen back to.
	if count, ok := countTopLevelArray(body); ok {
		return count, nil
	}

	var envelope struct {
		Jobs    json.RawMessage `json:"jobs"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, err
	}

	// Lever returns a bare array rather than an object, which the struct decode above rejects; it is
	// handled before falling through.
	var raw json.RawMessage
	switch {
	case len(envelope.Jobs) > 0:
		raw = envelope.Jobs
	case len(envelope.Content) > 0:
		// SmartRecruiters-style envelope, kept so a future vendor with the same shape works.
		raw = envelope.Content
	default:
		return 0, nil
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}

// countTopLevelArray handles a response whose body is a bare JSON array, which is Lever's shape.
func countTopLevelArray(body []byte) (int, bool) {
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "[") {
		return 0, false
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(body, &entries); err != nil {
		return 0, false
	}
	return len(entries), true
}
