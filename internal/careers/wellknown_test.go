// wellknown_test.go: tests for the robots.txt and sitemap parsers.
//
// The fixtures are reduced from the real files recorded during planning: Verizon's robots.txt names
// its careers host outright, and Pfizer's and Boeing's sitemaps list careers URLs.
package careers

import (
	"strings"
	"testing"
)

// verizonRobots is the shape that made this tier worth having: the careers host is named directly.
const verizonRobots = `User-agent: *
Disallow: /about/careers/terremark/
Disallow: /about/work/jobs/search?per_page=
Disallow: /content/dam/
Sitemap: https://mycareer.verizon.com/sitemap.xml
`

const plainRobots = `# a comment
User-agent: *
Disallow: /admin/
Allow: /
`

func TestParseRobotsFindsTheCareersSitemap(t *testing.T) {
	sitemaps, _ := ParseRobots(verizonRobots)
	if len(sitemaps) != 1 {
		t.Fatalf("found %d sitemaps, want 1: %+v", len(sitemaps), sitemaps)
	}
	if sitemaps[0].URL != "https://mycareer.verizon.com/sitemap.xml" {
		t.Errorf("sitemap = %q", sitemaps[0].URL)
	}
	if sitemaps[0].Source != "robots" {
		t.Errorf("source = %q, want robots", sitemaps[0].Source)
	}
}

// A Disallow on a careers path still names a path the site acknowledges.
func TestParseRobotsFindsCareersPaths(t *testing.T) {
	_, paths := ParseRobots(verizonRobots)
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "/about/careers/terremark/") {
		t.Errorf("the careers disallow was missed: %v", paths)
	}
	if !strings.Contains(joined, "/about/work/jobs/search?per_page=") {
		t.Errorf("the jobs disallow was missed: %v", paths)
	}
	// A disallow with no careers meaning is not a candidate.
	if strings.Contains(joined, "/content/dam/") {
		t.Errorf("an unrelated disallow was kept: %v", paths)
	}
}

func TestParseRobotsIgnoresUnrelatedRules(t *testing.T) {
	sitemaps, paths := ParseRobots(plainRobots)
	if len(sitemaps) != 0 {
		t.Errorf("found sitemaps in a file with none: %+v", sitemaps)
	}
	if len(paths) != 0 {
		t.Errorf("found careers paths in a file with none: %v", paths)
	}
}

func TestParseRobotsHandlesCommentsAndCase(t *testing.T) {
	body := "SITEMAP: https://example.com/sm.xml  # primary\nAllow: /Careers/\n"
	sitemaps, paths := ParseRobots(body)
	if len(sitemaps) != 1 || sitemaps[0].URL != "https://example.com/sm.xml" {
		t.Errorf("sitemaps = %+v, want the URL with the comment stripped", sitemaps)
	}
	if len(paths) != 1 || paths[0] != "/Careers/" {
		t.Errorf("paths = %v, want the mixed-case careers path", paths)
	}
}

func TestParseRobotsToleratesMalformedLines(t *testing.T) {
	// No colon, an empty value, and a bare colon: none should panic or produce a bogus entry.
	body := "not a directive\nSitemap:\n:\nSitemap: https://example.com/real.xml\n"
	sitemaps, _ := ParseRobots(body)
	if len(sitemaps) != 1 || sitemaps[0].URL != "https://example.com/real.xml" {
		t.Errorf("sitemaps = %+v, want only the well-formed one", sitemaps)
	}
}

const verizonSitemapIndex = `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://mycareer.verizon.com/sitemap-jobs.xml</loc></sitemap>
  <sitemap><loc>https://mycareer.verizon.com/sitemap-pages.xml</loc></sitemap>
</sitemapindex>`

const boeingUrlset = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://www.boeing.com/company/careers</loc></url>
  <url><loc>https://www.boeing.com/careers/privacy-statement</loc></url>
  <url><loc>https://www.boeing.com/products</loc></url>
</urlset>`

func TestParseSitemapDetectsAnIndex(t *testing.T) {
	locs, isIndex := ParseSitemap(verizonSitemapIndex)
	if !isIndex {
		t.Error("isIndex = false for a sitemapindex")
	}
	if len(locs) != 2 {
		t.Fatalf("locs = %v, want 2", locs)
	}
	if locs[0] != "https://mycareer.verizon.com/sitemap-jobs.xml" {
		t.Errorf("locs[0] = %q", locs[0])
	}
}

func TestParseSitemapDetectsAUrlset(t *testing.T) {
	locs, isIndex := ParseSitemap(boeingUrlset)
	if isIndex {
		t.Error("isIndex = true for a urlset")
	}
	if len(locs) != 3 {
		t.Errorf("locs = %v, want 3", locs)
	}
}

// A body that is neither is a dead end, not an error.
func TestParseSitemapRejectsUnrecognisedBodies(t *testing.T) {
	for _, body := range []string{"", "<html><body>not a sitemap</body></html>", `{"json":true}`} {
		locs, isIndex := ParseSitemap(body)
		if locs != nil || isIndex {
			t.Errorf("ParseSitemap(%q) = %v, %v; want no locations", body, locs, isIndex)
		}
	}
}

func TestCareersURLsFromSitemapPicksCareersPaths(t *testing.T) {
	locs, _ := ParseSitemap(boeingUrlset)
	got := CareersURLsFromSitemap(locs)
	// Boeing's sitemap lists /careers/privacy-statement as well as /company/careers. Both are one
	// path segment deep, so depth alone cannot separate them; the boilerplate filter does.
	if len(got) != 1 {
		t.Fatalf("got %v, want only the careers entry point", got)
	}
	if got[0] != "https://www.boeing.com/company/careers" {
		t.Errorf("got[0] = %q", got[0])
	}
	for _, u := range got {
		if strings.Contains(u, "/products") {
			t.Errorf("an unrelated URL was kept: %q", u)
		}
		if strings.Contains(u, "privacy") {
			t.Errorf("a boilerplate page was kept: %q", u)
		}
	}
}

// A supporting page under a careers path is not an entry point, even though it is shallow.
func TestCareersURLsFromSitemapDropsBoilerplateSubPages(t *testing.T) {
	locs := []string{
		"https://example.com/careers/privacy-statement",
		"https://example.com/careers/apply",
		"https://example.com/careers",
		"https://example.com/careers/faq",
	}
	got := CareersURLsFromSitemap(locs)
	if len(got) != 1 || got[0] != "https://example.com/careers" {
		t.Errorf("got %v, want only /careers", got)
	}
}

// A deep careers URL must lose to a shallow one, since the shallow path is the entry point.
func TestCareersURLsFromSitemapRanksShallowFirst(t *testing.T) {
	locs := []string{
		"https://example.com/careers/teams/engineering/locations/emea/opportunities",
		"https://example.com/jobs",
		"https://example.com/company/careers",
	}
	got := CareersURLsFromSitemap(locs)
	if len(got) != 3 {
		t.Fatalf("got %v, want 3", got)
	}
	if got[0] != "https://example.com/jobs" {
		t.Errorf("got[0] = %q, want the shallowest URL", got[0])
	}
	if got[2] != "https://example.com/careers/teams/engineering/locations/emea/opportunities" {
		t.Errorf("got[2] = %q, want the deepest last", got[2])
	}
}

func TestParseSitemapDecodesXMLEntities(t *testing.T) {
	body := `<urlset><url><loc>https://example.com/careers?a=1&amp;b=2</loc></url></urlset>`
	locs, _ := ParseSitemap(body)
	if len(locs) != 1 {
		t.Fatalf("locs = %v", locs)
	}
	if locs[0] != "https://example.com/careers?a=1&b=2" {
		t.Errorf("loc = %q, want the entity decoded", locs[0])
	}
}

// The sitemap search must be deterministic: two runs over the same input produce the same order.
func TestCareersURLsFromSitemapIsDeterministic(t *testing.T) {
	locs := []string{
		"https://example.com/company/careers",
		"https://example.com/jobs",
		"https://example.com/about/careers",
	}
	first := CareersURLsFromSitemap(append([]string(nil), locs...))
	for i := 0; i < 5; i++ {
		again := CareersURLsFromSitemap(append([]string(nil), locs...))
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("order changed between runs: %v vs %v", first, again)
			}
		}
	}
	// Same depth ties break lexically, so /about/careers precedes /company/careers.
	if first[0] != "https://example.com/jobs" {
		t.Errorf("first = %q", first[0])
	}
	if first[1] != "https://example.com/about/careers" || first[2] != "https://example.com/company/careers" {
		t.Errorf("tie-break order = %v", first[1:])
	}
}
