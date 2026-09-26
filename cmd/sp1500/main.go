// Command sp1500 fetches the three S&P index constituent pages and writes the companies they list
// into the shared jobs.db.
//
// One process invocation does all three pages. Each page is an independent run with its own
// scrape_runs row and its own raw capture, so a failure on one does not hide the others.
//
// Configuration comes from the environment, matching the existing scraper: DB_PATH, DATA_DIR,
// USER_AGENT, HTTP_TIMEOUT, LOG_LEVEL. There are no flags.
//
// design: docs/sp1500-plan.md - M1.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/runindex"
	"jobsapp/internal/sp1500"
)

// indexTarget is one source page: which index it carries, the platform row that identifies the run,
// the capture name, and the URL.
type indexTarget struct {
	index    sp1500.Index
	platform int64
	source   string
	url      string
}

// The platform ids are the reference rows seeded by migration 004.
var targets = []indexTarget{
	{sp1500.SP500, 20, "wikipedia_sp500", "/wiki/List_of_S%26P_500_companies"},
	{sp1500.SP400, 21, "wikipedia_sp400", "/wiki/List_of_S%26P_400_companies"},
	{sp1500.SP600, 22, "wikipedia_sp600", "/wiki/List_of_S%26P_600_companies"},
}

// defaultBaseURL is Wikipedia's host. SP1500_BASE_URL overrides it, which keeps the command's own
// wiring testable without a socket to the real site and lets a mirror be pointed at instead.
const defaultBaseURL = "https://en.wikipedia.org"

func baseURL() string {
	if v := strings.TrimSpace(os.Getenv("SP1500_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultBaseURL
}

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "status=error step=config err=%q\n", singleLine(err.Error()))
		return runindex.ExitConfig
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		// A migration failure leaves no scrape_runs row: the process never reached the network.
		fmt.Fprintf(os.Stderr, "status=error step=open err=%q\n", singleLine(err.Error()))
		return runindex.ExitConfig
	}
	defer func() { _ = database.Close() }()

	client := &http.Client{
		Timeout: cfg.HTTPTimeout,
		// Wikipedia's API etiquette expects a descriptive User-Agent on every request, so it
		// is attached at the transport rather than per request.
		Transport: userAgentTransport{agent: cfg.UserAgent, base: http.DefaultTransport},
	}

	// The worst outcome wins. Exit 5 is a success variant, so it must not be allowed to stand in
	// for a real failure on another page, but a real failure must not be reduced to exit 5 either.
	worst := runindex.ExitOK
	base := baseURL()
	for _, target := range targets {
		runner := runindex.Runner{
			DB:       database,
			Client:   client,
			DataDir:  cfg.DataDir,
			URL:      base + target.url,
			Index:    target.index,
			Source:   target.source,
			Platform: target.platform,
			Now:      time.Now,
		}
		res, err := runner.Run(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "run=%d source=%s status=error step=run err=%q exit=%d\n",
				res.RunID, res.Source, singleLine(err.Error()), res.ExitCode)
		} else {
			fmt.Printf("run=%d source=%s status=ok index=%s path=%s raw=%s found=%d inserted=%d duration_ms=%d\n",
				res.RunID, res.Source, res.Index, res.RawPath, res.RawState, res.Found, res.Inserted,
				res.Duration.Milliseconds())
		}
		worst = worseExit(worst, res.ExitCode)
	}
	return worst
}

// worseExit combines two exit codes. A genuine failure outranks the same-day-collision success
// variant, and the first failure is kept rather than overwritten by a later, larger one.
func worseExit(current, next int) int {
	if next == runindex.ExitOK {
		return current
	}
	if current == runindex.ExitOK {
		return next
	}
	// A failure already recorded stays; exit 5 never displaces or masks one.
	if current == runindex.ExitRawCollision {
		return next
	}
	return current
}

// userAgentTransport attaches one configured User-Agent to every request.
type userAgentTransport struct {
	agent string
	base  http.RoundTripper
}

func (t userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.agent != "" && req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.agent)
	}
	return t.base.RoundTrip(req)
}

// singleLine keeps one log line one line: the error text can carry newlines from the HTTP client.
func singleLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			out = append(out, ' ')
			continue
		}
		if r < 0x20 {
			continue
		}
		out = append(out, r)
	}
	const maxLen = 1000
	if len(out) > maxLen {
		out = out[:maxLen]
	}
	return string(out)
}
