// vendor.go: identify the applicant-tracking-system vendor behind a rendered careers page.
//
// The JSON-API vendors (Greenhouse, Lever, Ashby) are already classified by ParseTenant; this
// extends classification to the HTML-only vendors (Workday, iCIMS, SuccessFactors, Phenom,
// Eightfold, Taleo, Oracle Cloud), whose boards sit either on their own domains or on the company's
// first-party domain.
//
// Classification uses two signals: the page's final URL (a redirect to the board) and the page HTML
// (a board link, or a vendor-specific DOM signature). The return value is the vendor's
// platforms.name, so a caller resolves it to a platforms id with store.PlatformIDByName and no
// further mapping. "" means "no known vendor", the case a caller routes to the generic browser agent.
package careers

import (
	"net/url"
	"strings"
)

// Fingerprint identifies the vendor behind a rendered careers page from its final URL (after
// redirects) and its HTML body. The host is checked before the DOM because a redirect target is the
// most reliable signal; DOM signatures and board links are the fallback.
func Fingerprint(finalURL, html string) string {
	if v := vendorFromURL(finalURL); v != "" {
		return v
	}
	return vendorFromHTML(html)
}

func vendorFromURL(raw string) string {
	if _, ats, err := ParseTenant(raw); err == nil {
		return string(ats)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, e := range vendorHosts {
		if host == e.host || strings.HasSuffix(host, "."+e.host) {
			return e.vendor
		}
	}
	return ""
}

// vendorHosts maps a vendor's board host to its name. It is consulted two ways: suffix-matched
// against the final page hostname (a redirect to the board), and substring-matched against the page
// HTML (a first-party page linking to its board - the norm among the confirmed career_site_url
// values, none of which are ATS-hosted).
var vendorHosts = []struct {
	host   string
	vendor string
}{
	{"boards.greenhouse.io", "greenhouse"},
	{"job-boards.greenhouse.io", "greenhouse"},
	{"jobs.lever.co", "lever"},
	{"jobs.ashbyhq.com", "ashby"},
	{"myworkdayjobs.com", "workday"},
	{"icims.com", "icims"},
	{"taleo.net", "taleo"},
	{"oraclecloud.com", "oraclecloud"},
	{"successfactors.com", "successfactors"},
	{"sapsf.com", "successfactors"},
}

// vendorFromHTML classifies by DOM signature and board link. The eightfold and phenom signatures are
// verified against real boards (worker/vendor-playbooks); board-host references are checked before
// the one best-effort marker so a footer mention never outranks a real board link.
func vendorFromHTML(html string) string {
	lower := strings.ToLower(html)
	switch {
	case strings.Contains(html, `data-testid="position-query-search-search"`):
		return "eightfold"
	case strings.Contains(html, "data-ph-at-id="):
		return "phenom"
	}
	for _, e := range vendorHosts {
		if strings.Contains(lower, e.host) {
			return e.vendor
		}
	}
	if strings.Contains(lower, "careersitecompanyid") || strings.Contains(lower, "successfactors") {
		return "successfactors"
	}
	return ""
}
