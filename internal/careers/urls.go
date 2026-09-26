// urls.go: URL parsing and resolution used by the validation gate.
package careers

import (
	"fmt"
	"net/url"
	"strings"
)

// parseURL parses an absolute URL, rejecting anything that is not http or https.
//
// A javascript: or mailto: href is common in navigation and would otherwise be stored as though it
// were a careers page.
func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("no host in %q", raw)
	}
	return u, nil
}

// resolveURL joins a possibly-relative candidate against the page it was found on.
//
// An empty candidate resolves to "", not to the base: an <a href=""> or a bare "#" anchor is a
// navigation placeholder, and treating it as "the current page" would accept the homepage as its
// own careers link. That is the Lockheed decoy the plan records.
func resolveURL(base, candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || candidate == "#" || strings.HasPrefix(candidate, "#") {
		return "", fmt.Errorf("empty or fragment-only href %q", candidate)
	}

	// Reject non-http schemes before resolving, so a javascript: or mailto: link is never turned
	// into an absolute URL.
	lower := strings.ToLower(candidate)
	for _, scheme := range []string{"javascript:", "mailto:", "tel:", "data:", "sms:"} {
		if strings.HasPrefix(lower, scheme) {
			return "", fmt.Errorf("non-navigational href %q", candidate)
		}
	}

	baseURL, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	if baseURL.Scheme == "" || baseURL.Host == "" {
		return "", fmt.Errorf("base %q is not absolute", base)
	}

	ref, err := url.Parse(candidate)
	if err != nil {
		return "", err
	}
	resolved := baseURL.ResolveReference(ref)

	// Strip the fragment: it never identifies a distinct document, and keeping it makes two
	// spellings of one page look like two candidates.
	resolved.Fragment = ""
	return resolved.String(), nil
}

// hostOfURL returns a URL's hostname, or empty when it cannot be parsed.
//
// The gate uses it to let a host that names the company corroborate a page whose title does not:
// boeing.com/company/careers is not generic however generic its "Careers" title reads.
func hostOfURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := parseURL(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// SameHostAs reports whether a URL's host matches a bare hostname, such as the ATS host an accepted
// candidate redirects to. The wiring uses it to tell "the ladder accepted the board itself" from
// "the ladder accepted a branded page that redirects to the board", which decide differently when the
// board's API then refuses.
func SameHostAs(rawURL, host string) bool {
	if rawURL == "" || host == "" {
		return false
	}
	u, err := parseURL(rawURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), host)
}

// sameSite reports whether two URLs share a registrable-ish host, comparing the last two labels.
//
// The comparison is deliberately coarse. It is used to keep an anchor scan on the company's own
// site, where the alternatives are a company domain and an applicant-tracking vendor, and a coarse
// test cannot be fooled by "www." or a subdomain into treating an external link as internal.
func sameSite(a, b string) bool {
	ah, err := parseURL(a)
	if err != nil {
		return false
	}
	bh, err := parseURL(b)
	if err != nil {
		return false
	}
	return lastTwoLabels(ah.Hostname()) == lastTwoLabels(bh.Hostname())
}

func lastTwoLabels(host string) string {
	host = strings.ToLower(host)
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// trackingParams are query parameters that identify a campaign or referrer rather than a resource.
//
// They are stripped from an accepted URL before it is stored, because two spellings of one page look
// like two candidates on a later run: the eleven-company live run stored Salesforce's careers page as
// /company/careers/?bc=HL, which would not have matched the same page without the parameter.
//
// The strip happens at accept time, not at fetch time: the fetch must use exactly the href the page
// published, or a site that needs the parameter to serve the page would break.
var trackingParams = map[string]bool{
	"bc": true, "ref": true, "source": true,
	"fbclid": true, "gclid": true, "yclid": true, "msclkid": true,
	"_ga": true, "_gl": true, "_hsenc": true, "_hsmi": true,
}

// isTrackingParam reports whether a query parameter is a tracking parameter. Prefix families cover
// the ones that are namespaced rather than fixed, such as utm_source and mc_cid.
func isTrackingParam(name string) bool {
	lower := strings.ToLower(name)
	if trackingParams[lower] {
		return true
	}
	return strings.HasPrefix(lower, "utm_") || strings.HasPrefix(lower, "mc_")
}

// NormaliseURL returns the canonical form of a URL for storage: the fragment removed and campaign
// parameters dropped, with everything else - including unknown parameters - preserved.
//
// Unknown parameters are deliberately kept. A parameter this code does not recognise may be
// load-bearing, such as a tenant identifier, and dropping it would store a URL that does not resolve
// to the page that was actually validated.
func NormaliseURL(raw string) string {
	u, err := parseURL(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	if u.RawQuery == "" {
		return u.String()
	}

	values := u.Query()
	for name := range values {
		if isTrackingParam(name) {
			values.Del(name)
		}
	}
	// Encode sorts by key, so the result is stable across runs and two spellings converge.
	u.RawQuery = values.Encode()
	return u.String()
}
