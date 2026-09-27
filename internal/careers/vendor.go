// vendor.go: identify the applicant-tracking-system vendor behind a rendered careers page.
//
// The JSON-API vendors (Greenhouse, Lever, Ashby) are already classified by ParseTenant; this
// extends classification to the HTML-only vendors (Workday, iCIMS, SuccessFactors, Phenom,
// Eightfold, Taleo, Oracle Cloud) whose boards sit on the company's own domain and can only be told
// apart by host or DOM signature.
//
// The return value is the vendor's platforms.name, so a caller resolves it to a platforms id with
// store.PlatformIDByName and no further mapping. "" means "no known vendor", the case a caller
// routes to the generic browser agent.
package careers

import (
	"net/url"
	"strings"
)

// Fingerprint identifies the vendor behind a rendered careers page from its final URL (after
// redirects) and its HTML body. Hosts are checked before the DOM because a redirect target is the
// most reliable signal; DOM signatures are the fallback for boards that stay on a first-party domain.
func Fingerprint(finalURL, html string) string {
	if v := vendorFromURL(finalURL); v != "" {
		return v
	}
	return vendorFromHTML(html)
}

// vendorFromURL classifies by host: first the JSON-API vendors via ParseTenant, then the HTML-only
// board hosts.
func vendorFromURL(raw string) string {
	if _, ats, err := ParseTenant(raw); err == nil {
		return string(ats)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, e := range htmlVendorHosts {
		if host == e.suffix || strings.HasSuffix(host, "."+e.suffix) {
			return e.vendor
		}
	}
	return ""
}

// htmlVendorHosts maps HTML-only board hosts (suffix-matched, because vendors use per-tenant
// subdomains like acme.wd1.myworkdayjobs.com) to the vendor name.
var htmlVendorHosts = []struct {
	suffix string
	vendor string
}{
	{"myworkdayjobs.com", "workday"},
	{"icims.com", "icims"},
	{"taleo.net", "taleo"},
	{"oraclecloud.com", "oraclecloud"},
	{"successfactors.com", "successfactors"},
	{"sapsf.com", "successfactors"},
}

// vendorFromHTML classifies by DOM signature. The eightfold and phenom entries are verified against
// real boards (worker/vendor-playbooks); the rest are best-effort and should be promoted to verified
// once a real board confirms them.
func vendorFromHTML(html string) string {
	lower := strings.ToLower(html)
	switch {
	case strings.Contains(html, `data-testid="position-query-search-search"`):
		return "eightfold"
	case strings.Contains(html, "data-ph-at-id="):
		return "phenom"
	}
	switch {
	case strings.Contains(lower, "myworkdayjobs"):
		return "workday"
	case strings.Contains(lower, "careersitecompanyid") || strings.Contains(lower, "successfactors"):
		return "successfactors"
	case strings.Contains(lower, "icims"):
		return "icims"
	case strings.Contains(lower, "taleo"):
		return "taleo"
	}
	return ""
}
