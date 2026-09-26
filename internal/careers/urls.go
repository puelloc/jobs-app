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
