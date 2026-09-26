// anchors.go: find careers-link candidates in a fetched page.
//
// This is the workhorse of the resolution ladder. A company's own navigation is the most reliable
// statement of where its careers page is, so an anchor scan finds more correct answers than any
// guessed path. It is a pure function over HTML: no I/O, so it is tested against recorded pages.
//
// design: docs/sp1500-plan.md, section 7.1 tier 4, and the decoys recorded in 4.11.
package careers

import (
	"regexp"
	"sort"
	"strings"
)

// AnchorCandidate is one link worth fetching, with the reasons it was ranked where it was.
type AnchorCandidate struct {
	// URL is absolute, with the fragment removed.
	URL string
	// Text is the link's visible text, trimmed and whitespace-normalised.
	Text string
	// Score ranks candidates against each other. Higher is more likely to be the careers page.
	Score int
	// External reports whether the link leaves the company's site, which usually means an
	// applicant-tracking vendor. Those are kept rather than dropped: the branded careers page
	// frequently redirects to one anyway, and a vendor host is often the answer.
	External bool
	// Evidence records which signals fired, for the audit trail.
	Evidence []string
}

// Weights for the signals. Anchor text is the strongest evidence because it is what the company
// chose to call the page; a URL path is weaker because "careers" appears in unrelated paths
// ("career-path", "careers-in-tech"); being inside navigation is weaker still.
const (
	scoreTextExact  = 100 // the link text is exactly "Careers" / "Jobs"
	scoreTextSignal = 60  // the link text contains a careers word
	scoreURLSignal  = 25  // the URL path contains a careers word
	scoreNavRegion  = 15  // the link sits in a header, nav, or footer
	scoreExternal   = -20 // a vendor-hosted board ranks below the company's own page
)

// anchorRE matches one anchor element and captures its href and inner HTML.
var anchorRE = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)

// hrefAttrRE pulls the href out of an anchor's attribute blob. Single and double quotes both occur.
var hrefAttrRE = regexp.MustCompile(`(?i)\bhref\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)

// regionRE identifies the parts of a document a link appears in. Navigation and footer links carry
// more weight than a link buried in body copy, which is often a blog post about careers.
//
// The three elements are spelled out instead of using a backreference: Go's regexp is RE2 and does
// not support them. A non-greedy body to the nearest close tag of any of the three is close enough
// for scoring, and erring wide only ever adds weight to links that are already navigation.
var regionRE = regexp.MustCompile(`(?is)<(?:nav|header|footer)\b[^>]*>.*?</(?:nav|header|footer)>`)

// exactLinkTextRE matches a link whose text is exactly one of the labels, not merely containing it.
// Word boundaries matter: "Search Open Jobs" contains "jobs" but is a different label, and matching
// it as exact would wrongly tie it with a plain "Careers" link.
var exactLinkTextRE = regexp.MustCompile(`^(careers?|jobs?|join us|join our team|work with us|open positions|vacancies|careers page)$`)

func isExactLinkText(text string) bool {
	return exactLinkTextRE.MatchString(strings.ToLower(strings.TrimSpace(text)))
}

// navRegionTexts are broader phrases that only count when the link is inside a nav, header or
// footer. In body copy, "opportunities" and "employment" routinely mean something else.
var navRegionTexts = []string{"opportunit", "employment", "hiring", "work for us", "join"}

// Extract returns careers-link candidates from a page, best first.
//
// pageURL is the URL the HTML was fetched from; it is the base for relative links.
func Extract(pageURL, html string) []AnchorCandidate {
	regions := navRegionRanges(html)

	type seen struct {
		index int
	}
	byURL := map[string]seen{}
	var out []AnchorCandidate

	for _, m := range anchorRE.FindAllStringSubmatchIndex(html, -1) {
		attrs := html[m[2]:m[3]]
		inner := html[m[4]:m[5]]

		href := hrefFrom(attrs)
		if href == "" {
			continue
		}
		// A fragment-only or empty href is a navigation placeholder. resolveURL refuses it, which
		// is the Lockheed "Careers" link whose href is "#".
		abs, err := resolveURL(pageURL, href)
		if err != nil {
			continue
		}

		text := normaliseSpace(tagStripper.ReplaceAllString(inner, " "))
		textLower := strings.ToLower(text)
		path := pathOf(abs)
		inRegion := inNavRegion(m[0], regions)

		var evidence []string
		score := 0

		switch {
		case isExactLinkText(textLower):
			score += scoreTextExact
			evidence = append(evidence, "text_exact:"+textLower)
		case hasCareersSignal(text):
			score += scoreTextSignal
			evidence = append(evidence, "text_signal")
		}

		if hasCareersPathToken(path) {
			score += scoreURLSignal
			evidence = append(evidence, "url_signal")
		}

		// A vague word only counts inside navigation: in body copy "opportunities" and
		// "employment" routinely mean something else.
		if inRegion && score == 0 {
			lower := strings.ToLower(text)
			for _, phrase := range navRegionTexts {
				if strings.Contains(lower, phrase) {
					score += scoreTextSignal
					evidence = append(evidence, "nav_text:"+phrase)
					break
				}
			}
		}

		// A link with no careers signal at all is noise, however prominent it is: "About us",
		// "Products", a social icon, a skip link. The navigation bonus is only ever added on top
		// of a signal, never as one, or every link in the header would become a candidate.
		if score == 0 {
			continue
		}

		if inRegion {
			score += scoreNavRegion
			evidence = append(evidence, "in_nav")
		}

		// A vendor-hosted board is a correct answer but a worse one: the plan prefers a branded
		// careers subdomain when both validate. The page a company points at from its own site is
		// the entry point it publishes, so it outranks a third-party host.
		external := !sameSite(pageURL, abs)
		if external {
			score += scoreExternal
			evidence = append(evidence, "external")
		}

		// Keep the highest score for a URL that appears more than once. The same careers link is
		// usually in both the header and the footer.
		if prev, dup := byURL[abs]; dup {
			if score > out[prev.index].Score {
				out[prev.index].Score = score
				out[prev.index].Evidence = evidence
				out[prev.index].Text = text
			}
			continue
		}
		byURL[abs] = seen{index: len(out)}
		out = append(out, AnchorCandidate{
			URL:      abs,
			Text:     text,
			Score:    score,
			External: external,
			Evidence: evidence,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// Ties break on the shorter URL, which prefers "example.com/careers" over
		// "example.com/careers/teams/engineering/opportunities".
		if len(out[i].URL) != len(out[j].URL) {
			return len(out[i].URL) < len(out[j].URL)
		}
		return out[i].URL < out[j].URL
	})
	return out
}

// hrefFrom pulls the href value out of an anchor's attributes, preferring double quotes.
func hrefFrom(attrs string) string {
	m := hrefAttrRE.FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	switch {
	case m[2] != "":
		return m[2]
	case m[3] != "":
		return m[3]
	default:
		return m[4]
	}
}

// navRegionRanges returns the start/end offsets of nav, header and footer elements.
func navRegionRanges(html string) [][2]int {
	var ranges [][2]int
	for _, m := range regionRE.FindAllStringIndex(html, -1) {
		ranges = append(ranges, [2]int{m[0], m[1]})
	}
	return ranges
}

func inNavRegion(offset int, ranges [][2]int) bool {
	for _, r := range ranges {
		if offset >= r[0] && offset < r[1] {
			return true
		}
	}
	return false
}

// pathOf returns the lowercased path of a URL, or "" when it cannot be parsed.
func pathOf(raw string) string {
	u, err := parseURL(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Path)
}

// hasCareersPathToken reports whether any path segment begins with a careers word.
func hasCareersPathToken(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		for _, token := range []string{"career", "job", "join", "employment", "opportunit", "hiring", "vacanc"} {
			if strings.HasPrefix(segment, token) {
				return true
			}
		}
	}
	return false
}
