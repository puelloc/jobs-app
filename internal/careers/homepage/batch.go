// batch.go: batched lookups against Wikipedia and Wikidata.
//
// Both sources are addressed 50 titles at a time rather than one call per company, which is the
// difference between ~30 requests and ~3,000 for the S&P 1500. Every call goes through the same
// Fetcher seam as the rest of the ladder, so the rate limiter applies and the tests need no socket.
//
// design: docs/sp1500-plan.md, sections 4.7 and 7.1 tier 1.
package homepage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"jobsapp/internal/careers"
)

// BatchSize is the per-call title limit both APIs accept.
const BatchSize = 50

// WikipediaEndpoint and WikidataEndpoint are the API roots. They are fields on the client rather
// than constants so a test can point them at an httptest server.
const (
	WikipediaEndpoint = "https://en.wikipedia.org"
	WikidataEndpoint  = "https://www.wikidata.org"
)

// Client resolves homepages from Wikipedia and Wikidata.
type Client struct {
	Fetcher careers.Fetcher
	// Wikipedia and Wikidata are the API roots; empty means the defaults.
	Wikipedia string
	Wikidata  string
}

// Source records which lookup produced a homepage.
type Source string

const (
	SourceInfobox Source = "wikipedia_infobox"
	SourceP856    Source = "wikidata_p856"
)

// Result is the outcome of resolving one company's homepage.
type Result struct {
	URL    string
	Source Source
	// Article is the enwiki title the value came from, which is also the key the careers tiers use
	// to match Wikidata later.
	Article string
	// Rank is the Wikidata rank of the chosen value, empty for an infobox result.
	Rank string
	// Disagreement is true when both sources produced a homepage and they were not the same host.
	// The infobox still wins - it is a single canonical value rather than a set - but a disagreement
	// is worth recording, because it means the two sources describe the company differently.
	Disagreement bool
	// OtherURL is the losing source's value when Disagreement is set.
	OtherURL string
}

// Resolve looks up homepages for every title, preferring the infobox and falling back to Wikidata.
//
// The returned map is keyed by the title as supplied. A title with no result is absent from the map
// rather than present with an empty value, so a caller cannot mistake "not looked up" for
// "looked up and found nothing".
func (c *Client) Resolve(ctx context.Context, titles []string) (map[string]Result, error) {
	out := map[string]Result{}
	if len(titles) == 0 {
		return out, nil
	}
	if c.Fetcher == nil {
		return nil, fmt.Errorf("homepage: Client.Fetcher is nil")
	}

	infobox, err := c.WikipediaWebsites(ctx, titles)
	if err != nil {
		return nil, err
	}
	entities, err := c.WikidataWebsites(ctx, titles)
	if err != nil {
		return nil, err
	}

	for _, title := range titles {
		var chosen *Result

		if info, ok := infobox[title]; ok && info != nil {
			chosen = &Result{URL: info.URL, Source: SourceInfobox, Article: info.Article}
		}
		if wd, ok := entities[title]; ok && wd != nil {
			sel := SelectHomepage(title, wd.Homepages)
			if sel.URL != "" {
				if chosen == nil {
					chosen = &Result{URL: sel.URL, Source: SourceP856, Article: wd.Article, Rank: sel.Rank}
				} else if !sameHost(chosen.URL, sel.URL) {
					// Both sources answered and they disagree. The infobox keeps the value
					// because it is a single canonical website rather than a set needing a
					// heuristic, but the disagreement is recorded for the audit trail.
					chosen.Disagreement = true
					chosen.OtherURL = sel.URL
				}
			}
		}

		if chosen != nil {
			out[title] = *chosen
		}
	}
	return out, nil
}

// --- Wikidata -------------------------------------------------------------

// Entity is the subset of a Wikidata entity the selection needs.
type Entity struct {
	QID       string
	Article   string
	Homepages []Candidate
}

// WikidataWebsites fetches P856 values for the given enwiki titles.
func (c *Client) WikidataWebsites(ctx context.Context, titles []string) (map[string]*Entity, error) {
	out := map[string]*Entity{}
	root := c.Wikidata
	if root == "" {
		root = WikidataEndpoint
	}

	for _, batch := range chunk(titles, BatchSize) {
		q := url.Values{}
		q.Set("action", "wbgetentities")
		q.Set("sites", "enwiki")
		q.Set("titles", strings.Join(batch, "|"))
		q.Set("props", "claims|sitelinks")
		q.Set("sitefilter", "enwiki")
		q.Set("format", "json")
		endpoint := root + "/w/api.php?" + q.Encode()

		resp, err := c.Fetcher.Fetch(ctx, endpoint, careers.HTMLMaxBodyBytes*4)
		if err != nil {
			return nil, fmt.Errorf("wikidata fetch: %w", err)
		}
		if resp.Status != 200 || len(resp.Body) == 0 {
			// A failed batch is skipped rather than fatal: the infobox may already have answered
			// for these titles, and aborting the whole run over one bad batch is worse than
			// leaving a residue for a later tier.
			continue
		}

		var decoded wikidataResponse
		if err := json.Unmarshal(resp.Body, &decoded); err != nil {
			return nil, fmt.Errorf("wikidata decode: %w", err)
		}
		for qid, entity := range decoded.Entities {
			// The response is keyed by QID, so the title has to be recovered from the sitelink.
			title := entity.Sitelinks.Enwiki.Title
			if title == "" {
				continue
			}
			out[title] = &Entity{
				QID:       qid,
				Article:   title,
				Homepages: p856Candidates(entity),
			}
		}
	}
	return out, nil
}

// p856Candidates extracts the P856 values and their ranks.
func p856Candidates(e wikidataEntity) []Candidate {
	claims, ok := e.Claims["P856"]
	if !ok {
		return nil
	}
	out := make([]Candidate, 0, len(claims))
	for _, claim := range claims {
		value, ok := claim.Mainsnak.Datavalue.Value.(string)
		if !ok || value == "" {
			continue
		}
		rank := claim.Rank
		if rank == "" {
			rank = "normal"
		}
		out = append(out, Candidate{URL: value, Rank: rank})
	}
	return out
}

type wikidataResponse struct {
	Entities map[string]wikidataEntity `json:"entities"`
}

type wikidataEntity struct {
	Claims    map[string][]wikidataClaim `json:"claims"`
	Sitelinks struct {
		Enwiki struct {
			Title string `json:"title"`
		} `json:"enwiki"`
	} `json:"sitelinks"`
}

type wikidataClaim struct {
	Mainsnak struct {
		Datavalue struct {
			Value any `json:"value"`
		} `json:"datavalue"`
	} `json:"mainsnak"`
	Rank string `json:"rank"`
}

// --- Wikipedia infobox ----------------------------------------------------

// InfoboxWebsite is a homepage read from an article's infobox.
type InfoboxWebsite struct {
	URL     string
	Article string
}

// WikipediaWebsites reads the infobox Website row from each article's wikitext.
//
// Batched through action=query&prop=revisions rather than action=parse, which would be one call per
// article. A redirect resolves through action=query automatically, so an alias title still answers.
func (c *Client) WikipediaWebsites(ctx context.Context, titles []string) (map[string]*InfoboxWebsite, error) {
	out := map[string]*InfoboxWebsite{}
	root := c.Wikipedia
	if root == "" {
		root = WikipediaEndpoint
	}

	for _, batch := range chunk(titles, BatchSize) {
		q := url.Values{}
		q.Set("action", "query")
		q.Set("prop", "revisions")
		q.Set("rvprop", "content")
		q.Set("rvslots", "main")
		q.Set("titles", strings.Join(batch, "|"))
		q.Set("redirects", "1")
		q.Set("format", "json")
		q.Set("formatversion", "2")
		endpoint := root + "/w/api.php?" + q.Encode()

		// Wikitext is larger than the HTML the gate reads, so the bound is raised.
		resp, err := c.Fetcher.Fetch(ctx, endpoint, careers.HTMLMaxBodyBytes*8)
		if err != nil {
			return nil, fmt.Errorf("wikipedia fetch: %w", err)
		}
		if resp.Status != 200 || len(resp.Body) == 0 {
			continue
		}

		var decoded wikipediaResponse
		if err := json.Unmarshal(resp.Body, &decoded); err != nil {
			return nil, fmt.Errorf("wikipedia decode: %w", err)
		}
		for _, page := range decoded.Query.Pages {
			if page.Missing {
				continue
			}
			if len(page.Revisions) == 0 {
				continue
			}
			wikitext := page.Revisions[0].Slots.Main.Content
			raw := infoboxWebsite(wikitext)
			if raw == "" {
				continue
			}
			out[page.Title] = &InfoboxWebsite{URL: raw, Article: page.Title}
		}
	}
	return out, nil
}

type wikipediaResponse struct {
	Query struct {
		Pages []struct {
			Title     string `json:"title"`
			Missing   bool   `json:"missing"`
			Revisions []struct {
				Slots struct {
					Main struct {
						Content string `json:"content"`
					} `json:"main"`
				} `json:"slots"`
			} `json:"revisions"`
		} `json:"pages"`
	} `json:"query"`
}

// websiteFieldRE finds the infobox's website parameter. The value runs to the end of the line, which
// is how the parameter is written in every infobox on these pages.
var websiteFieldRE = regexp.MustCompile(`(?im)^\s*\|\s*website\s*=\s*(.*)$`)

// urlInWikitextRE finds the first http(s) URL in a wikitext fragment.
var urlInWikitextRE = regexp.MustCompile(`https?://[^\s\]\}<>"|]+`)

// infoboxWebsite extracts a URL from the infobox website parameter.
//
// Three spellings occur: a bare URL, an {{URL|...}} or {{Official website|...}} template, and a
// bracketed external link. The URL is taken from whichever is present, with trailing punctuation
// trimmed because the field often ends a sentence.
func infoboxWebsite(wikitext string) string {
	m := websiteFieldRE.FindStringSubmatch(wikitext)
	if m == nil {
		return ""
	}
	value := strings.TrimSpace(m[1])
	if value == "" {
		return ""
	}
	// A comment or a bare "none" is not a website.
	if strings.HasPrefix(value, "<!--") || strings.EqualFold(value, "none") {
		return ""
	}
	found := urlInWikitextRE.FindString(value)
	if found == "" {
		return ""
	}
	return strings.TrimRight(found, ".,;:)")
}

// --- helpers --------------------------------------------------------------

// chunk splits titles into batches without copying the input.
func chunk(items []string, size int) [][]string {
	if size <= 0 {
		size = BatchSize
	}
	var out [][]string
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[i:end])
	}
	return out
}

// sameHost compares registrable-ish hosts, so www.example.com and example.com agree.
func sameHost(a, b string) bool {
	ua, err := parseURL(a)
	if err != nil {
		return false
	}
	ub, err := parseURL(b)
	if err != nil {
		return false
	}
	return registrable(ua.Hostname()) == registrable(ub.Hostname())
}

func registrable(host string) string {
	host = strings.ToLower(host)
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// SortedTitles returns titles in a stable order, so two runs issue the same batches.
func SortedTitles(titles []string) []string {
	out := append([]string(nil), titles...)
	sort.Strings(out)
	return out
}
