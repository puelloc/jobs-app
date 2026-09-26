// sources.go: the closed vocabularies for where a resolved value came from.
//
// companies.website_source and companies.career_site_url_source are separate from the Outcome enum
// because they answer a different question. Outcome says what happened to a candidate; Source says
// which tier produced the value that was stored. A tier that has no Source value is a tier that does
// not exist yet, which is what keeps the list honest.
//
// design: docs/sp1500-plan.md, section 4.5.
package store

// WebsiteSource names the tier that produced companies.website.
type WebsiteSource string

const (
	// WebsiteSourceWikipediaInfobox is the infobox Website row, read from the article's wikitext.
	// Preferred over Wikidata because it is a single canonical value rather than a set.
	WebsiteSourceWikipediaInfobox WebsiteSource = "wikipedia_infobox"
	// WebsiteSourceWikidataP856 is Wikidata's official-website property, which needs a selection
	// heuristic: Apple alone carries 109 values, mostly regional storefronts.
	WebsiteSourceWikidataP856 WebsiteSource = "wikidata_p856"
	// WebsiteSourceSEC10K is the corporate homepage as published in a 10-K. Deferred out of this
	// chunk; the value exists so the column cannot be written with a source that has no tier.
	WebsiteSourceSEC10K WebsiteSource = "sec_10k"
)

// AllWebsiteSources lists every declared value, so a test can hold the set closed.
func AllWebsiteSources() []WebsiteSource {
	return []WebsiteSource{
		WebsiteSourceWikipediaInfobox,
		WebsiteSourceWikidataP856,
		WebsiteSourceSEC10K,
	}
}

// IsValid reports whether the value is declared.
func (s WebsiteSource) IsValid() bool {
	for _, v := range AllWebsiteSources() {
		if s == v {
			return true
		}
	}
	return false
}

// CareerSiteSource names the tier that produced companies.career_site_url.
type CareerSiteSource string

const (
	// CareerSiteSourceAnchorScan is the company's own navigation, the workhorse tier.
	CareerSiteSourceAnchorScan CareerSiteSource = "anchor_scan"
	// CareerSiteSourceSitemap is a careers URL found in sitemap.xml.
	CareerSiteSourceSitemap CareerSiteSource = "sitemap"
	// CareerSiteSourceRobotsSignal is a path taken from a robots.txt rule that names a careers
	// section. It is a signal, not a permission: robots never gates a request here.
	CareerSiteSourceRobotsSignal CareerSiteSource = "robots_signal"
	// CareerSiteSourceCommonPath is a guessed conventional path such as /careers.
	CareerSiteSourceCommonPath CareerSiteSource = "common_path"
	// CareerSiteSourceATSAPI is a validated applicant-tracking board read from its JSON API.
	CareerSiteSourceATSAPI CareerSiteSource = "ats_api"
	// CareerSiteSourceATSHTML is a validated applicant-tracking tenant read as HTML. M2b.
	CareerSiteSourceATSHTML CareerSiteSource = "ats_html"
	// CareerSiteSourceBrowserUse is the M3 worker. Declared so the value exists before the tier
	// does, and so a run cannot record a source that has no tier.
	CareerSiteSourceBrowserUse CareerSiteSource = "browser_use"
)

// AllCareerSiteSources lists every declared value.
func AllCareerSiteSources() []CareerSiteSource {
	return []CareerSiteSource{
		CareerSiteSourceAnchorScan,
		CareerSiteSourceSitemap,
		CareerSiteSourceRobotsSignal,
		CareerSiteSourceCommonPath,
		CareerSiteSourceATSAPI,
		CareerSiteSourceATSHTML,
		CareerSiteSourceBrowserUse,
	}
}

// IsValid reports whether the value is declared.
func (s CareerSiteSource) IsValid() bool {
	for _, v := range AllCareerSiteSources() {
		if s == v {
			return true
		}
	}
	return false
}
