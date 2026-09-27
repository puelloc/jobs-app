// Package robots implements the polite-crawler half of the scraping policy: fetch and parse a
// site's robots.txt, answer "may I fetch this path", and surface the site's requested crawl-delay.
//
// It is deliberately small. The full sweep runs a browser agent and a render pass against hundreds
// of boards, and robots.txt is the one signal a board can give us about what it will tolerate. The
// policy engine here is the pure, unit-tested core; cmd/scrape is the wiring.
package robots

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxBodyBytes bounds a robots.txt read. A robots.txt larger than this is treated as unreadable.
const maxBodyBytes = 1 << 20

// Policy is the parsed decision set for one origin: which paths may be fetched, and how long the
// site asks between requests.
type Policy struct {
	crawlDelay time.Duration
	groups     []group
}

type group struct {
	agents []string // lowercased user-agent tokens; "*" matches every agent
	rules  []rule
	delay  time.Duration
}

type rule struct {
	allow   bool
	pattern string
}

// Parse parses robots.txt content. Only the fields this flow acts on are kept - user-agent,
// disallow, allow and crawl-delay - and everything else (sitemap, host, request-rate, comments,
// unknown fields) is ignored. Malformed lines are skipped, never fatal: robots.txt is advisory, so
// a typo must not turn a parse into a hard failure that blocks the crawl.
func Parse(body string) *Policy {
	p := &Policy{}
	var cur *group
	flush := func() {
		if cur != nil {
			p.groups = append(p.groups, *cur)
			cur = nil
		}
	}

	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		field, value, ok := splitField(line)
		if !ok {
			continue
		}
		switch strings.ToLower(field) {
		case "user-agent":
			flush()
			cur = &group{agents: []string{strings.ToLower(strings.TrimSpace(value))}}
		case "disallow", "allow":
			if cur == nil {
				cur = &group{} // a rule before any user-agent line still parses; it just has no agent
			}
			cur.rules = append(cur.rules, rule{
				allow:   strings.EqualFold(field, "allow"),
				pattern: strings.TrimSpace(value),
			})
		case "crawl-delay":
			if cur == nil {
				cur = &group{}
			}
			if d, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && d >= 0 {
				cur.delay = time.Duration(d * float64(time.Second))
			}
		}
	}
	flush()
	return p
}

// splitField splits "Field: value" on the first colon. The value may itself contain colons, so only
// the first is the separator.
func splitField(line string) (string, string, bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

// Allowed reports whether the path of raw may be fetched under the group selected for ua. A policy
// with no matching group (an empty or absent robots.txt) allows everything.
func (p *Policy) Allowed(raw, ua string) bool {
	g := p.matchGroup(ua)
	path := requestPath(raw)

	// The most specific matching rule wins; on equal length the least restrictive (allow) wins,
	// per RFC 9309 §2.2.2. No match means nothing disallows it.
	best := -1
	bestAllow := true
	for _, r := range g.rules {
		if !matchPattern(r.pattern, path) {
			continue
		}
		if len(r.pattern) > best || (len(r.pattern) == best && r.allow) {
			best = len(r.pattern)
			bestAllow = r.allow
		}
	}
	if best == -1 {
		return true
	}
	return bestAllow
}

// CrawlDelay returns the selected group's requested delay between requests. Zero means the site
// asked for none, or no group matched.
func (p *Policy) CrawlDelay(ua string) time.Duration {
	return p.matchGroup(ua).delay
}

// matchGroup returns the group that governs ua: the most specific user-agent line that matches,
// falling back to the "*" group, then to an empty group (allow everything, no delay).
func (p *Policy) matchGroup(ua string) group {
	want := strings.ToLower(ua)
	var fallback *group
	for i := range p.groups {
		g := &p.groups[i]
		for _, agent := range g.agents {
			if agent == "*" {
				if fallback == nil {
					fallback = g
				}
				continue
			}
			// A robots.txt user-agent token is a case-insensitive substring of the product token.
			if agent != "" && strings.Contains(want, agent) {
				return *g
			}
		}
	}
	if fallback != nil {
		return *fallback
	}
	return group{}
}

// requestPath extracts the path component used for rule matching. A parse failure or empty path
// falls back to "/", so every URL still matches the common Disallow: / pattern.
func requestPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return "/"
	}
	return u.Path
}

// matchPattern reports whether a robots.txt pattern matches a request path. It supports the two
// wildcards the standard carries over from the original proposal: "*" (any run, including empty)
// and a trailing "$" (anchor to the end of the path). A pattern without a wildcard is a prefix.
func matchPattern(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")

	if !strings.Contains(pattern, "*") {
		if anchored {
			return path == pattern
		}
		return strings.HasPrefix(path, pattern)
	}

	segs := strings.Split(pattern, "*")
	pos := 0
	for i, seg := range segs {
		if seg == "" {
			continue
		}
		idx := strings.Index(path[pos:], seg)
		if idx < 0 {
			return false
		}
		// The first segment must sit at the start unless the pattern opens with "*".
		if i == 0 && !strings.HasPrefix(path, seg) {
			return false
		}
		pos += idx + len(seg)
	}
	if anchored {
		return strings.HasSuffix(path, segs[len(segs)-1])
	}
	return true
}

// Fetch retrieves and parses the robots.txt for the origin of raw, using ua as the User-Agent on
// the request (the same string later selects the rule group). HTTP outcomes map to the
// conservative behavior the standard prescribes: a 200 parses; a 404/410 means no robots.txt and
// allows everything; any other status or a network error denies everything - better to skip a site
// than to crawl one whose policy we could not read. Only a malformed raw (not http(s), no host) is
// returned as an error rather than a policy.
func Fetch(ctx context.Context, client *http.Client, raw, ua string) (*Policy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("robots: parse %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("robots: %q is not an http(s) URL", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("robots: %q has no host", raw)
	}
	if client == nil {
		client = http.DefaultClient
	}

	robotsURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("robots: build request for %s: %w", u.Host, err)
	}
	req.Header.Set("User-Agent", ua)

	resp, err := client.Do(req)
	if err != nil {
		return denyAll(), nil
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if err != nil {
			return denyAll(), nil
		}
		return Parse(string(body)), nil
	case http.StatusNotFound, http.StatusGone:
		return allowAll(), nil
	default:
		return denyAll(), nil
	}
}

// denyAll is the conservative policy for an unreadable robots.txt.
func denyAll() *Policy {
	return &Policy{groups: []group{{
		agents: []string{"*"},
		rules:  []rule{{allow: false, pattern: "/"}},
	}}}
}

// allowAll is the policy for an origin with no robots.txt.
func allowAll() *Policy {
	return &Policy{}
}
