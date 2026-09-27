// robots_test.go: tests for the robots.txt policy engine.
package robots

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const ua = "jobs-app/0.1 (+https://github.com/local/jobs-app)"

func TestParse_DisallowPrefix(t *testing.T) {
	p := Parse("User-agent: *\nDisallow: /api/\n")
	if p.Allowed("https://example.com/api/jobs", ua) {
		t.Error("path under /api/ should be disallowed")
	}
	if !p.Allowed("https://example.com/careers", ua) {
		t.Error("path outside /api/ should be allowed")
	}
}

func TestParse_AllowOverridesDisallow(t *testing.T) {
	p := Parse("User-agent: *\nDisallow: /search\nAllow: /search/public\n")
	if !p.Allowed("https://example.com/search/public", ua) {
		t.Error("Allow: /search/public should beat Disallow: /search")
	}
	if p.Allowed("https://example.com/search/private", ua) {
		t.Error("/search/private should still be disallowed")
	}
}

func TestParse_LongestMatchWins(t *testing.T) {
	p := Parse("User-agent: *\nDisallow: /job\nAllow: /job/feed\n")
	if !p.Allowed("https://example.com/job/feed", ua) {
		t.Error("longer Allow should win over shorter Disallow")
	}
	if p.Allowed("https://example.com/job/other", ua) {
		t.Error("Disallow: /job should still cover /job/other")
	}
}

func TestParse_EqualLengthAllowWins(t *testing.T) {
	// Two patterns of the same length, one disallow one allow: the allow wins (RFC 9309).
	p := Parse("User-agent: *\nDisallow: /x\nAllow: /x\n")
	if !p.Allowed("https://example.com/x", ua) {
		t.Error("equal-length Allow must beat Disallow")
	}
}

func TestParse_WildcardAndAnchor(t *testing.T) {
	p := Parse("User-agent: *\nDisallow: /*.pdf$\n")
	if p.Allowed("https://example.com/a/b/report.pdf", ua) {
		t.Error("/*.pdf$ should disallow report.pdf")
	}
	if !p.Allowed("https://example.com/report.pdf.html", ua) {
		t.Error("/*.pdf$ must not match a path not ending in .pdf")
	}
}

func TestParse_EmptyPolicyAllowsEverything(t *testing.T) {
	p := Parse("")
	if !p.Allowed("https://example.com/anything", ua) {
		t.Error("empty robots.txt allows everything")
	}
	if got := p.CrawlDelay(ua); got != 0 {
		t.Errorf("empty policy crawl-delay = %v, want 0", got)
	}
}

func TestParse_CrawlDelay(t *testing.T) {
	p := Parse("User-agent: *\nCrawl-delay: 3\n")
	if got := p.CrawlDelay(ua); got != 3*time.Second {
		t.Errorf("crawl-delay = %v, want 3s", got)
	}
}

func TestParse_SpecificUserAgentBeatsStar(t *testing.T) {
	p := Parse("User-agent: *\nDisallow: /\n\nUser-agent: jobs-app\nDisallow:\n")
	if !p.Allowed("https://example.com/careers", ua) {
		t.Error("a specific user-agent group must override the * group")
	}
	if p.Allowed("https://example.com/careers", "other-bot/1.0") {
		t.Error("the * group's Disallow: / must still govern other agents")
	}
}

func TestParse_AgentTokenIsSubstring(t *testing.T) {
	p := Parse("User-agent: jobs-app\nDisallow: /private\n")
	if !p.Allowed("https://example.com/public", ua) {
		t.Error("UA jobs-app/0.1 matches the jobs-app group")
	}
	if p.Allowed("https://example.com/private", ua) {
		t.Error("/private should be disallowed for the jobs-app group")
	}
}

func TestFetch_OKParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/robots.txt" {
			t.Errorf("path = %q, want /robots.txt", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got != ua {
			t.Errorf("User-Agent = %q, want %q", got, ua)
		}
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /private\nCrawl-delay: 2\n"))
	}))
	defer srv.Close()

	p, err := Fetch(context.Background(), srv.Client(), srv.URL+"/careers", ua)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if p.Allowed(srv.URL+"/private", ua) {
		t.Error("/private should be disallowed")
	}
	if got := p.CrawlDelay(ua); got != 2*time.Second {
		t.Errorf("crawl-delay = %v, want 2s", got)
	}
}

func TestFetch_NotFoundAllowsAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p, err := Fetch(context.Background(), srv.Client(), srv.URL+"/careers", ua)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !p.Allowed(srv.URL+"/anything", ua) {
		t.Error("a 404 robots.txt allows everything")
	}
}

func TestFetch_ServerErrorDeniesAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p, err := Fetch(context.Background(), srv.Client(), srv.URL+"/careers", ua)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if p.Allowed(srv.URL+"/anything", ua) {
		t.Error("an errored robots.txt must deny everything")
	}
}

func TestFetch_RejectsMalformedURL(t *testing.T) {
	if _, err := Fetch(context.Background(), nil, "ftp://example.com/x", ua); err == nil {
		t.Error("a non-http(s) URL should be an error")
	}
	if _, err := Fetch(context.Background(), nil, "not a url", ua); err == nil {
		t.Error("a malformed URL should be an error")
	}
}
