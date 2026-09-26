// batch_test.go: tests for the batched Wikipedia and Wikidata lookups.
//
// The fixtures are the real response shapes, reduced: Wikidata keys entities by QID and the title
// must be recovered from the sitelink, and Wikipedia returns wikitext through the revisions
// property rather than parsed HTML.
package homepage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"jobsapp/internal/careers"
)

// apiStub serves canned API responses and counts calls per path.
type apiStub struct {
	mu      sync.Mutex
	calls   map[string]int
	titles  [][]string // the titles= parameter of each call, in order
	handler func(kind, titles string) (int, []byte)
}

func newAPIStub(h func(kind, titles string) (int, []byte)) *apiStub {
	return &apiStub{calls: map[string]int{}, handler: h}
}

func (s *apiStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		kind := q.Get("action")
		titles := q.Get("titles")

		s.mu.Lock()
		s.calls[kind]++
		s.titles = append(s.titles, strings.Split(titles, "|"))
		s.mu.Unlock()

		status, body := s.handler(kind, titles)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

func (s *apiStub) callCount(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[kind]
}

func (s *apiStub) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, 0, len(s.titles))
	for _, b := range s.titles {
		out = append(out, len(b))
	}
	return out
}

// stubFetch adapts the API stub to the seam, so the client is exercised through the real interface.
//
// Both fields default to the same httptest server. A test that sets only one of them would otherwise
// send the other source's request to an empty URL, which fails as a transport error and looks like a
// production bug rather than a test setup mistake - exactly what happened the first time.
type stubFetch struct {
	wikipedia, wikidata string
	recorder            *careers.RecordingFetcher
}

// target routes on the action parameter, not on the host.
//
// The first version keyed on whether the URL contained "en.wikipedia.org", which is true in
// production but false against an httptest server on 127.0.0.1 - so both sources resolved to the
// same handler and every batch came back as the Wikidata response. Routing on the query the client
// actually sends is both correct and independent of where the server happens to live.
func (f stubFetch) target(rawURL string) string {
	action := ""
	if parsed, err := url.Parse(rawURL); err == nil {
		action = parsed.Query().Get("action")
	}
	switch action {
	case "query":
		if f.wikipedia != "" {
			return f.wikipedia
		}
		return f.wikidata
	case "wbgetentities":
		if f.wikidata != "" {
			return f.wikidata
		}
		return f.wikipedia
	default:
		if f.wikipedia != "" {
			return f.wikipedia
		}
		return f.wikidata
	}
}

func (f stubFetch) Fetch(ctx context.Context, rawURL string, maxBodyBytes int64) (careers.Response, error) {
	f.recorder.Record(rawURL, maxBodyBytes)
	server := f.target(rawURL)
	if server == "" {
		return careers.Response{FinalURL: rawURL, Status: 0,
			TransportError: fmt.Errorf("test stub has no server for %s", rawURL)}, nil
	}
	// Redirect only the origin. Swapping the whole URL for the server's root discards the path and
	// the query, so the request arrives with no action and no titles - which is what happened the
	// first time, and it presented as a parser bug rather than a stub bug.
	rewritten, err := rewriteOrigin(rawURL, server)
	if err != nil {
		return careers.Response{FinalURL: rawURL, Status: 0, TransportError: err}, nil
	}
	return get(ctx, rewritten, maxBodyBytes)
}

// rewriteOrigin replaces the scheme and host of rawURL with those of server, keeping the path and
// query.
func rewriteOrigin(rawURL, server string) (string, error) {
	orig, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	base, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	orig.Scheme = base.Scheme
	orig.Host = base.Host
	return orig.String(), nil
}

func get(ctx context.Context, rawURL string, maxBodyBytes int64) (careers.Response, error) {
	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return careers.Response{TransportError: err}, nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return careers.Response{TransportError: err}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if int64(len(buf)) > maxBodyBytes {
				buf = buf[:maxBodyBytes]
				return careers.Response{FinalURL: rawURL, Status: resp.StatusCode,
					ContentType: resp.Header.Get("Content-Type"), Body: buf, Truncated: true}, nil
			}
		}
		if err != nil {
			break
		}
	}
	return careers.Response{FinalURL: rawURL, Status: resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"), Body: buf}, nil
}

// --- Wikidata -------------------------------------------------------------

// wikidataBody builds a response keyed by QID with the title only in the sitelink, which is the
// detail that costs a naive implementation its matches.
func wikidataBody(titles []string, values map[string][]Candidate) []byte {
	entities := map[string]any{}
	for i, title := range titles {
		qid := fmt.Sprintf("Q%d", 1000+i)
		var claims []any
		for _, c := range values[title] {
			claims = append(claims, map[string]any{
				"mainsnak": map[string]any{
					"datavalue": map[string]any{"value": c.URL, "type": "string"},
				},
				"rank": c.Rank,
			})
		}
		entities[qid] = map[string]any{
			"claims": map[string]any{"P856": claims},
			"sitelinks": map[string]any{
				"enwiki": map[string]any{"title": title},
			},
		}
	}
	body, _ := json.Marshal(map[string]any{"entities": entities})
	return body
}

// wikipediaBody builds a revisions response with the given wikitext per title.
func wikipediaBody(wikitext map[string]string) []byte {
	var pages []any
	for title, text := range wikitext {
		pages = append(pages, map[string]any{
			"title": title,
			"revisions": []any{
				map[string]any{"slots": map[string]any{"main": map[string]any{"content": text}}},
			},
		})
	}
	body, _ := json.Marshal(map[string]any{"query": map[string]any{"pages": pages}})
	return body
}

func TestWikidataP856BatchReturnsEntitiesKeyedByTitle(t *testing.T) {
	stub := newAPIStub(func(kind, titles string) (int, []byte) {
		switch kind {
		case "wbgetentities":
			return 200, wikidataBody(strings.Split(titles, "|"), map[string][]Candidate{
				"Apple Inc.": {{URL: "https://apple.com/", Rank: "normal"}},
			})
		default:
			return 200, wikipediaBody(nil)
		}
	})
	srv := stub.server(t)
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikidata: srv.URL}, Wikidata: srv.URL, Wikipedia: srv.URL}
	got, err := c.WikidataWebsites(context.Background(), []string{"Apple Inc."})
	if err != nil {
		t.Fatalf("WikidataWebsites: %v", err)
	}
	entity, ok := got["Apple Inc."]
	if !ok {
		t.Fatalf("Apple Inc. missing from %v", got)
	}
	if entity.QID != "Q1000" {
		t.Errorf("QID = %q", entity.QID)
	}
	if len(entity.Homepages) != 1 || entity.Homepages[0].URL != "https://apple.com/" {
		t.Errorf("homepages = %+v", entity.Homepages)
	}
}

// 60 titles must go out as two calls of 50 and 10, not sixty calls.
func TestInfoboxBatchFetches50TitlesPerCall(t *testing.T) {
	var titles []string
	for i := 0; i < 60; i++ {
		titles = append(titles, fmt.Sprintf("Company %d", i))
	}

	stub := newAPIStub(func(kind, t2 string) (int, []byte) {
		return 200, wikipediaBody(nil)
	})
	srv := stub.server(t)
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikipedia: srv.URL}, Wikipedia: srv.URL, Wikidata: srv.URL}
	if _, err := c.WikipediaWebsites(context.Background(), titles); err != nil {
		t.Fatalf("WikipediaWebsites: %v", err)
	}

	if got := stub.callCount("query"); got != 2 {
		t.Errorf("made %d calls for 60 titles, want 2", got)
	}
	sizes := stub.batchSizes()
	if len(sizes) != 2 || sizes[0] != 50 || sizes[1] != 10 {
		t.Errorf("batch sizes = %v, want [50 10]", sizes)
	}
}

func TestWikidataBatchFetches50TitlesPerCall(t *testing.T) {
	var titles []string
	for i := 0; i < 120; i++ {
		titles = append(titles, fmt.Sprintf("Company %d", i))
	}

	stub := newAPIStub(func(kind, t string) (int, []byte) {
		return 200, wikidataBody(nil, nil)
	})
	srv := stub.server(t)
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikidata: srv.URL}, Wikipedia: srv.URL, Wikidata: srv.URL}
	if _, err := c.WikidataWebsites(context.Background(), titles); err != nil {
		t.Fatalf("WikidataWebsites: %v", err)
	}
	sizes := stub.batchSizes()
	want := []int{50, 50, 20}
	if len(sizes) != 3 {
		t.Fatalf("made %d calls, want 3 (sizes %v)", len(sizes), sizes)
	}
	for i := range want {
		if sizes[i] != want[i] {
			t.Errorf("batch %d size = %d, want %d", i, sizes[i], want[i])
		}
	}
}

// --- infobox extraction ---------------------------------------------------

func TestInfoboxExtractsWebsiteRowFromWikitext(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wikitext string
		want     string
	}{
		{
			"bare url",
			"{{Infobox company\n| name = Apple Inc.\n| website = https://apple.com\n| founded = 1977\n}}",
			"https://apple.com",
		},
		{
			"URL template",
			"{{Infobox company\n| website = {{URL|https://aaon.com}}\n}}",
			"https://aaon.com",
		},
		{
			"official website template",
			"{{Infobox company\n| website = {{Official website|http://example.com}}\n}}",
			"http://example.com",
		},
		{
			"bracketed external link",
			"{{Infobox company\n| website = [https://example.com/ Example Corp]\n}}",
			"https://example.com/",
		},
		{
			"trailing punctuation stripped",
			"{{Infobox company\n| website = https://example.com.\n}}",
			"https://example.com",
		},
		{
			"empty value",
			"{{Infobox company\n| website =\n}}",
			"",
		},
		{
			"html comment instead of a value",
			"{{Infobox company\n| website = <!-- none -->\n}}",
			"",
		},
		{
			"no website row at all",
			"{{Infobox company\n| name = Example\n}}",
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := infoboxWebsite(tc.wikitext); got != tc.want {
				t.Errorf("infoboxWebsite = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- the combined resolver ------------------------------------------------

func TestInfoboxWinsOverWikidataOnDisagreement(t *testing.T) {
	stub := newAPIStub(func(kind, titles string) (int, []byte) {
		switch kind {
		case "wbgetentities":
			// A host the heuristic would itself choose, so the disagreement is real rather than
			// an artifact of the other value being rejected. An investor.* host would have been
			// dropped by the heuristic and the test would have asserted nothing.
			return 200, wikidataBody(strings.Split(titles, "|"), map[string][]Candidate{
				"Apple Inc.": {{URL: "https://apple-corp.example/", Rank: "normal"}},
			})
		default:
			return 200, wikipediaBody(map[string]string{
				"Apple Inc.": "{{Infobox company\n| website = https://apple.com\n}}",
			})
		}
	})
	srv := stub.server(t)
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikipedia: srv.URL, wikidata: srv.URL}, Wikipedia: srv.URL, Wikidata: srv.URL}
	got, err := c.Resolve(context.Background(), []string{"Apple Inc."})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res, ok := got["Apple Inc."]
	if !ok {
		t.Fatalf("Apple Inc. missing from %v", got)
	}
	if res.Source != SourceInfobox {
		t.Errorf("Source = %q, want the infobox to win", res.Source)
	}
	if res.URL != "https://apple.com" {
		t.Errorf("URL = %q, want the infobox value", res.URL)
	}
	if !res.Disagreement {
		t.Error("Disagreement = false; the two sources named different hosts and that should be recorded")
	}
	if res.OtherURL != "https://apple-corp.example/" {
		t.Errorf("OtherURL = %q, want the losing value", res.OtherURL)
	}
}

func TestHomepageSourceDisagreementRecordedAsRejected(t *testing.T) {
	// Different paths on the same host are agreement, not disagreement.
	stub := newAPIStub(func(kind, titles string) (int, []byte) {
		switch kind {
		case "wbgetentities":
			return 200, wikidataBody(strings.Split(titles, "|"), map[string][]Candidate{
				"Example Corp": {{URL: "https://example.com/", Rank: "preferred"}},
			})
		default:
			return 200, wikipediaBody(map[string]string{
				"Example Corp": "{{Infobox company\n| website = https://www.example.com/\n}}",
			})
		}
	})
	srv := stub.server(t)
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikipedia: srv.URL, wikidata: srv.URL}, Wikipedia: srv.URL, Wikidata: srv.URL}
	got, err := c.Resolve(context.Background(), []string{"Example Corp"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got["Example Corp"].Disagreement {
		t.Error("Disagreement = true for two spellings of the same host")
	}
}

// Wikidata is used when the infobox has nothing.
func TestFallsBackToWikidataWhenTheInfoboxIsSilent(t *testing.T) {
	stub := newAPIStub(func(kind, titles string) (int, []byte) {
		switch kind {
		case "wbgetentities":
			return 200, wikidataBody(strings.Split(titles, "|"), map[string][]Candidate{
				"Example Corp": {{URL: "https://example.com/", Rank: "preferred"}},
			})
		default:
			return 200, wikipediaBody(map[string]string{
				"Example Corp": "{{Infobox company\n| name = Example\n}}",
			})
		}
	})
	srv := stub.server(t)
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikipedia: srv.URL, wikidata: srv.URL}, Wikipedia: srv.URL, Wikidata: srv.URL}
	got, err := c.Resolve(context.Background(), []string{"Example Corp"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res, ok := got["Example Corp"]
	if !ok {
		t.Fatal("Example Corp missing: Wikidata should have answered")
	}
	if res.Source != SourceP856 || res.URL != "https://example.com/" {
		t.Errorf("got %+v, want the Wikidata value", res)
	}
}

// A company neither source knows about is absent from the map, not present and empty, so a caller
// cannot mistake "not looked up" for "looked up and found nothing".
func TestUnknownCompanyIsAbsentFromTheResultMap(t *testing.T) {
	stub := newAPIStub(func(kind, titles string) (int, []byte) {
		switch kind {
		case "wbgetentities":
			return 200, []byte(`{"entities":{}}`)
		default:
			return 200, wikipediaBody(nil)
		}
	})
	srv := stub.server(t)
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikipedia: srv.URL, wikidata: srv.URL}, Wikipedia: srv.URL, Wikidata: srv.URL}
	got, err := c.Resolve(context.Background(), []string{"Nonexistent Corp"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, present := got["Nonexistent Corp"]; present {
		t.Errorf("Nonexistent Corp is present with %+v, want absent", got["Nonexistent Corp"])
	}
}

// A failing batch is skipped rather than aborting the run: the infobox may already have answered.
func TestAFailedBatchDoesNotAbortTheResolve(t *testing.T) {
	stub := newAPIStub(func(kind, titles string) (int, []byte) {
		switch kind {
		case "wbgetentities":
			return 500, []byte("server error")
		default:
			return 200, wikipediaBody(map[string]string{
				"Example Corp": "{{Infobox company\n| website = https://example.com\n}}",
			})
		}
	})
	srv := stub.server(t)
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikipedia: srv.URL, wikidata: srv.URL}, Wikipedia: srv.URL, Wikidata: srv.URL}
	got, err := c.Resolve(context.Background(), []string{"Example Corp"})
	if err != nil {
		t.Fatalf("Resolve returned an error for a failed Wikidata batch: %v", err)
	}
	if got["Example Corp"].URL != "https://example.com" {
		t.Errorf("the infobox result was lost: %+v", got)
	}
}

// The tier-1 fetch goes through the seam, so the rate limiter sees Wikipedia and Wikidata as the
// separate hosts they are.
func TestTierOneBatchesGoThroughTheSeam(t *testing.T) {
	stub := newAPIStub(func(kind, titles string) (int, []byte) {
		return 200, wikipediaBody(nil)
	})
	srv := stub.server(t)
	defer srv.Close()

	rec := careers.NewRecordingFetcher()
	c := &Client{Fetcher: rec.Handler(stubFetch{wikipedia: srv.URL, wikidata: srv.URL}),
		Wikipedia: srv.URL, Wikidata: srv.URL}
	if _, err := c.Resolve(context.Background(), []string{"A", "B"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.Count() != 2 {
		t.Errorf("seam saw %d fetches, want 2 (one per source)", rec.Count())
	}
	for _, call := range rec.Calls() {
		if call.MaxBodyBytes <= 0 {
			t.Errorf("fetch %s passed maxBodyBytes %d, want a positive bound", call.URL, call.MaxBodyBytes)
		}
	}
}

// The API URL must carry the titles parameter encoded, or a title containing & or a comma breaks the
// request.
func TestBatchTitlesAreURLEncoded(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write(wikipediaBody(nil))
	}))
	defer srv.Close()

	c := &Client{Fetcher: stubFetch{wikipedia: srv.URL}, Wikipedia: srv.URL, Wikidata: srv.URL}
	titles := []string{"AT&T", "Johnson & Johnson"}
	if _, err := c.WikipediaWebsites(context.Background(), titles); err != nil {
		t.Fatalf("WikipediaWebsites: %v", err)
	}
	if got := gotQuery.Get("titles"); got != "AT&T|Johnson & Johnson" {
		t.Errorf("titles = %q, want the raw value encoded and decoded back", got)
	}
	if gotQuery.Get("redirects") != "1" {
		t.Error("redirects=1 is missing, so an alias title would not resolve")
	}
}
