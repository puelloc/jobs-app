// Package homepage resolves a company's corporate homepage from Wikipedia and Wikidata.
//
// This is tier 1 of the careers ladder, and it runs only for companies whose website is unknown. It
// is a separate package because it is a different problem from finding a careers page: it is
// resolving an identity, not a destination.
//
// The Wikidata side deserves its warning. P856 ("official website") is heavily polluted: Apple
// carries 109 values, mostly regional storefronts such as apple.com/az/ and apple.com/om-ar/, and
// none of them is marked preferred. Taking the first value, or the only value when there appears to
// be one, produces a regional landing page rather than the company. The selection heuristic here is
// what turns that set into a homepage, and rank - the rule that looks like it should solve the
// problem - is a no-op for exactly that case.
//
// design: docs/sp1500-plan.md, sections 4.7 and 7.1 tier 1.
package homepage

import (
	"net/url"
	"sort"
	"strings"

	"jobsapp/internal/careers"
)

// Ranks for the selection heuristic. Higher wins.
//
// The order encodes what was measured, not what seems tidy: rank solves roughly 180 of 1,237
// entities and not Apple, so it sits below the path-shape rules that do the real work.
const (
	rankPreferred   = 1000
	rankBarePath    = 100 // the URL is a bare origin, e.g. https://apple.com/
	rankHttps       = 20
	rankShortPath   = 5   // per segment of advantage over the longest candidate
	rankPlainScheme = 1   // small nod to the scheme without letting it outweigh path shape
	penaltyNonBare  = -10 // a path exists at all
)

// Selection is the outcome of choosing a homepage from a set of candidates.
type Selection struct {
	// URL is the chosen homepage, or empty when nothing was usable.
	URL string
	// Rank is the Wikidata rank of the chosen value: preferred, normal, or deprecated.
	Rank string
	// Bare reports whether the chosen URL had no path, which is the shape a corporate homepage
	// usually has.
	Bare bool
	// Rejected counts how many candidates were dropped as unusable, for the audit trail.
	Rejected int
	// Reason explains an empty URL.
	Reason string
}

// SelectHomepage chooses the best corporate homepage from a set of candidate URLs.
//
// companyName is used to prefer a candidate whose host actually names the company. It may be empty,
// in which case the token check is skipped rather than failing - the caller decides what to do about
// an unverifiable result, the same division of responsibility the validation gate uses.
func SelectHomepage(companyName string, candidates []Candidate) Selection {
	if len(candidates) == 0 {
		return Selection{Reason: "no_candidates"}
	}

	token := distinctiveToken(companyName)

	type scored struct {
		url   string
		rank  string
		score int
		bare  bool
		host  bool // the host names the company
	}

	var usable []scored
	rejected := 0

	for _, c := range candidates {
		u, err := parseURL(c.URL)
		if err != nil {
			rejected++
			continue
		}
		// A deprecated value is a statement that the URL is wrong. Dropping it is the one part of
		// the rank rule that is unconditional.
		if c.Rank == "deprecated" {
			rejected++
			continue
		}
		if isLocaleOnly(u) {
			// A storefront is not a homepage. This is the rule that rescues Apple.
			rejected++
			continue
		}
		if isNonHomepagePath(u) {
			// Support, investor relations, careers and shop paths are real pages, but they are
			// not the site's front door.
			rejected++
			continue
		}

		bare := isBareOrigin(u)
		s := 0
		if c.Rank == "preferred" {
			s += rankPreferred
		}
		if bare {
			s += rankBarePath
		} else {
			s += penaltyNonBare
		}
		if u.Scheme == "https" {
			s += rankHttps
		} else {
			s += rankPlainScheme
		}
		// A shallow path beats a deep one.
		s -= rankShortPath * pathDepth(u)
		// Fewer subdomains is closer to the front door.
		s -= rankShortPath * max(0, strings.Count(u.Hostname(), ".")-1)

		hostMatches := token != "" && strings.Contains(strings.ToLower(u.Hostname()), token)
		if hostMatches {
			s += rankBarePath
		}

		usable = append(usable, scored{url: u.String(), rank: c.Rank, score: s, bare: bare, host: hostMatches})
	}

	if len(usable) == 0 {
		return Selection{Rejected: rejected, Reason: "no_usable_candidate"}
	}

	sort.SliceStable(usable, func(i, j int) bool {
		if usable[i].score != usable[j].score {
			return usable[i].score > usable[j].score
		}
		if usable[i].host != usable[j].host {
			return usable[i].host
		}
		// Deterministic final tie-break, so two runs over the same data agree.
		if len(usable[i].url) != len(usable[j].url) {
			return len(usable[i].url) < len(usable[j].url)
		}
		return usable[i].url < usable[j].url
	})

	best := usable[0]
	return Selection{URL: best.url, Rank: best.rank, Bare: best.bare, Rejected: rejected}
}

// Candidate is one homepage URL with the metadata the selection needs.
type Candidate struct {
	URL string
	// Rank is the Wikidata rank: "preferred", "normal", or "deprecated". Empty for a source that
	// has no notion of rank, such as a Wikipedia infobox, which yields a single value.
	Rank string
}

// localeSegment matches a bare language or region qualifier: "en", "en-us", "pt_BR".
// It is duplicated from internal/careers rather than exported from there because that copy is
// unexported and the two serve different callers; a divergence would show up as a locale path being
// accepted here, which has its own test.
func localeLike(segment string) bool {
	if len(segment) < 2 || len(segment) > 5 {
		return false
	}
	for _, r := range segment {
		switch {
		case r == '-' || r == '_':
			continue
		case r >= 'a' && r <= 'z':
			continue
		default:
			return false
		}
	}
	return true
}

// isLocaleOnly reports whether every path segment is a locale qualifier.
func isLocaleOnly(u *url.URL) bool {
	segments := pathSegments(u)
	if len(segments) == 0 {
		return false
	}
	for _, s := range segments {
		if !localeLike(s) {
			return false
		}
	}
	return true
}

// isBareOrigin reports whether the URL is just a scheme and host, with no meaningful path.
func isBareOrigin(u *url.URL) bool {
	return strings.Trim(u.Path, "/") == ""
}

func pathSegments(u *url.URL) []string {
	var out []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}

func pathDepth(u *url.URL) int { return len(pathSegments(u)) }

// nonHomepageSegments are path segments that mark a page other than the front door. A website
// property pointing at one of these is not wrong exactly, but a bare origin beats it.
var nonHomepageSegments = map[string]bool{
	"investor": true, "investors": true, "investor-relations": true, "ir": true,
	"news": true, "newsroom": true, "press": true, "media": true,
	"support": true, "help": true, "helpdesk": true, "contact": true, "contact-us": true,
	"shop": true, "store": true, "careers": true, "jobs": true, "about": true,
}

func isNonHomepagePath(u *url.URL) bool {
	for _, s := range pathSegments(u) {
		if nonHomepageSegments[s] {
			return true
		}
	}
	return false
}

// distinctiveToken mirrors the validation gate's helper. The gate's copy is unexported, so rather
// than duplicate the logic and risk the two drifting, this delegates through an exported shim that
// the careers package provides for exactly this purpose.
func distinctiveToken(company string) string { return careers.DistinctiveToken(company) }

// parseURL accepts only http and https, matching the gate's rule so a mailto: or javascript: value
// cannot become a company's website.
func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errUnsupportedScheme
	}
	if u.Host == "" {
		return nil, errNoHost
	}
	return u, nil
}

var (
	errUnsupportedScheme = errorString("unsupported scheme")
	errNoHost            = errorString("no host")
)

type errorString string

func (e errorString) Error() string { return string(e) }
