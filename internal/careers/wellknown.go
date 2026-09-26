// wellknown.go: tier 3 of the resolution ladder - robots.txt and sitemap.xml.
//
// Both are cheap, official, and sometimes name the careers host outright: Verizon's robots.txt
// carries "Sitemap: https://mycareer.verizon.com/sitemap.xml", which is a direct answer that no
// amount of scanning the homepage would find. Pfizer's and Boeing's sitemaps list careers URLs.
//
// These are parsers, not fetchers. They take the text and return candidates, so every rule is
// testable against recorded files and the caller owns the network.
//
// design: docs/sp1500-plan.md, section 7.1 tier 3.
package careers

import (
	"regexp"
	"strings"
)

// SitemapRef is a sitemap location found in robots.txt or inside a sitemap index.
type SitemapRef struct {
	URL string
	// Source records where it was found: "robots" or "sitemap_index".
	Source string
}

// ParseRobots extracts the careers-relevant declarations from a robots.txt body.
//
// It returns any Sitemap: locations, which are worth fetching in full, and any Allow/Disallow paths
// that look like careers paths, which are worth probing directly. A Disallow is still a useful
// pointer: it is a path the site acknowledges exists.
func ParseRobots(body string) (sitemaps []SitemapRef, paths []string) {
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "sitemap":
			sitemaps = append(sitemaps, SitemapRef{URL: value, Source: "robots"})
		case "allow", "disallow":
			if pathLooksLikeCareers(value) {
				paths = append(paths, value)
			}
		}
	}
	return sitemaps, paths
}

// stripComment removes a trailing "#" comment. A "#" inside a URL is rare in robots.txt and treating
// it as a comment is what the specification says.
func stripComment(line string) string {
	if i := strings.Index(line, "#"); i >= 0 {
		return line[:i]
	}
	return line
}

// pathLooksLikeCareers reports whether a robots.txt path names a careers section.
func pathLooksLikeCareers(path string) bool {
	lower := strings.ToLower(path)
	for _, token := range []string{"career", "job", "employment", "vacanc", "hiring", "opportunit"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

var (
	// locRE matches a <loc> element in either a urlset or a sitemapindex.
	locRE = regexp.MustCompile(`(?is)<loc\s*>\s*(.*?)\s*</loc>`)
	// sitemapIndexRE distinguishes an index from a urlset.
	sitemapIndexRE = regexp.MustCompile(`(?is)<sitemapindex\b`)
	// urlsetRE marks a document that actually lists pages.
	urlsetRE = regexp.MustCompile(`(?is)<urlset\b`)
)

// ParseSitemap returns the locations listed in a sitemap or sitemap index.
//
// isIndex reports which kind the document is, because the two need different treatment: an index's
// locations are more sitemaps to fetch, while a urlset's are pages, and only a urlset is worth
// searching for careers URLs.
//
// A document that is neither returns no locations rather than an error: the caller is probing, and
// an unrecognised body is a dead end, not a failure.
func ParseSitemap(body string) (locs []string, isIndex bool) {
	if !sitemapIndexRE.MatchString(body) && !urlsetRE.MatchString(body) {
		return nil, false
	}
	for _, m := range locRE.FindAllStringSubmatch(body, -1) {
		loc := decodeXMLEntities(strings.TrimSpace(m[1]))
		if loc != "" {
			locs = append(locs, loc)
		}
	}
	return locs, sitemapIndexRE.MatchString(body)
}

// CareersURLsFromSitemap returns the locations that look like careers pages, best first.
//
// Sitemap order is not priority order, so the results are ranked by depth: a shallow "/careers" is
// more likely to be the entry point than a deep
// "/careers/teams/engineering/locations/emea/opportunities".
//
// Boilerplate sub-pages that sit under a careers path are dropped. Boeing's sitemap lists
// /careers/privacy-statement next to /company/careers, and the first is not an entry point however
// shallow it is.
func CareersURLsFromSitemap(locs []string) []string {
	var hits []string
	for _, loc := range locs {
		path := pathOf(loc)
		if isBoilerplatePath(path) {
			continue
		}
		if hasCareersPathToken(path) || pathLooksLikeCareers(path) {
			hits = append(hits, loc)
		}
	}
	sortByPathDepth(hits)
	return hits
}

// boilerplateSegments are path segments that mark a supporting page rather than an entry point.
// They appear *under* a careers path - privacy notices, EEO statements, accessibility pages - and
// would otherwise rank highly because they are shallow.
//
// This is a rejection list, not a depth heuristic, because depth does not separate them: Boeing's
// /careers/privacy-statement sits at the same depth as /company/careers. What distinguishes them is
// that the segment names a document rather than a place to find work.
//
// Matching is exact on the lowercased segment. A company that genuinely publishes /careers/diversity
// as its entry point loses to any other candidate, which is the right trade: a missed candidate
// costs one more fetch, whereas a wrong pick is stored and trusted downstream.
var boilerplateSegments = map[string]bool{
	"privacy": true, "privacy-statement": true, "privacy-policy": true, "privacy-notice": true,
	"terms": true, "terms-of-use": true, "terms-and-conditions": true, "terms-conditions": true,
	"legal": true, "legal-notice": true, "disclaimer": true, "disclosures": true,
	"cookies": true, "cookie-policy": true, "cookie-notice": true,
	"accessibility": true, "accessibility-statement": true, "accessibility-policy": true,
	"eeo": true, "eeo-statement": true, "eeo-policy": true, "equal-opportunity": true,
	"diversity": true, "diversity-statement": true, "diversity-policy": true,
	"accommodations": true, "accommodation": true, "veterans": true, "disability": true,
	"faq": true, "faqs": true, "contact": true, "contact-us": true,
	"sitemap": true, "rss": true, "search": true, "login": true, "signin": true, "sign-in": true,
	"apply": true, "application": true, "application-status": true, "candidate-privacy": true,
}

// isBoilerplatePath reports whether any path segment names a supporting page.
func isBoilerplatePath(path string) bool {
	for _, segment := range strings.Split(strings.ToLower(path), "/") {
		if boilerplateSegments[segment] {
			return true
		}
	}
	return false
}

// sortByPathDepth orders URLs by the number of non-empty path segments, then lexically, so the
// result is deterministic.
func sortByPathDepth(urls []string) {
	depth := func(raw string) int {
		n := 0
		for _, s := range strings.Split(pathOf(raw), "/") {
			if s != "" {
				n++
			}
		}
		return n
	}
	// A hand-rolled insertion sort keeps this dependency-free and is fine for the tens of URLs a
	// careers search returns.
	for i := 1; i < len(urls); i++ {
		for j := i; j > 0; j-- {
			less := false
			switch {
			case depth(urls[j]) < depth(urls[j-1]):
				less = true
			case depth(urls[j]) == depth(urls[j-1]) && urls[j] < urls[j-1]:
				less = true
			}
			if !less {
				break
			}
			urls[j], urls[j-1] = urls[j-1], urls[j]
		}
	}
}

// decodeXMLEntities handles the escapes that appear inside <loc> values. Only the ones that occur
// in URLs are needed.
func decodeXMLEntities(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&apos;", "'",
	)
	return replacer.Replace(s)
}
