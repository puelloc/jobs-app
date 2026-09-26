// main_test.go: tests for the exit-code combination and the log-line sanitiser.
//
// The interesting behaviour is worseExit. Exit 5 is a success variant - the run did its database
// work, but the UTC day already had a capture - so it must not be reported as a failure, and it
// must not mask a genuine failure on another page either.
package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jobsapp/internal/db"
	"jobsapp/internal/runindex"
)

func TestWorseExitKeepsOKWhenNothingFailed(t *testing.T) {
	if got := worseExit(runindex.ExitOK, runindex.ExitOK); got != runindex.ExitOK {
		t.Errorf("worseExit(0, 0) = %d, want 0", got)
	}
}

func TestWorseExitReportsTheFirstFailure(t *testing.T) {
	if got := worseExit(runindex.ExitOK, runindex.ExitFailure); got != runindex.ExitFailure {
		t.Errorf("worseExit(0, 1) = %d, want 1", got)
	}
}

// A failure on one page is not reduced by a later page reporting only a same-day collision.
func TestWorseExitKeepsFailureOverCollision(t *testing.T) {
	if got := worseExit(runindex.ExitFailure, runindex.ExitRawCollision); got != runindex.ExitFailure {
		t.Errorf("worseExit(1, 5) = %d, want 1: exit 5 must not mask a failure", got)
	}
}

// And the collision must not be *reported* when a failure happened elsewhere.
func TestWorseExitDoesNotReportCollisionWhenAFailureHappened(t *testing.T) {
	got := worseExit(runindex.ExitFailure, runindex.ExitRawCollision)
	if got == runindex.ExitRawCollision {
		t.Fatal("worseExit reported exit 5 while a failure was already recorded")
	}
}

func TestWorseExitReportsCollisionWhenEverythingElseSucceeded(t *testing.T) {
	if got := worseExit(runindex.ExitOK, runindex.ExitRawCollision); got != runindex.ExitRawCollision {
		t.Errorf("worseExit(0, 5) = %d, want 5", got)
	}
}

func TestWorseExitKeepsCollisionAcrossLaterSuccess(t *testing.T) {
	if got := worseExit(runindex.ExitRawCollision, runindex.ExitOK); got != runindex.ExitRawCollision {
		t.Errorf("worseExit(5, 0) = %d, want 5", got)
	}
}

func TestWorseExitKeepsTheFirstOfTwoFailures(t *testing.T) {
	// The earlier failure is the one worth reporting; a later, larger code should not replace it.
	if got := worseExit(runindex.ExitFailure, runindex.ExitEmpty); got != runindex.ExitFailure {
		t.Errorf("worseExit(1, 4) = %d, want 1", got)
	}
}

func TestSingleLineEscapesNewlines(t *testing.T) {
	got := singleLine("first\nsecond\r\nthird\ttabbed")
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("singleLine left a control character in %q", got)
	}
	if got != "first second  third tabbed" {
		t.Errorf("singleLine = %q", got)
	}
}

func TestSingleLineStripsOtherControlCharacters(t *testing.T) {
	got := singleLine("a\x00b\x1bc")
	if got != "abc" {
		t.Errorf("singleLine = %q, want %q", got, "abc")
	}
}

func TestSingleLineTruncates(t *testing.T) {
	got := singleLine(strings.Repeat("x", 1500))
	if len(got) != 1000 {
		t.Errorf("len = %d, want 1000", len(got))
	}
}

func TestSingleLineLeavesShortTextAlone(t *testing.T) {
	if got := singleLine("plain"); got != "plain" {
		t.Errorf("singleLine = %q, want plain", got)
	}
}

// Every index page must have a distinct platform row and capture name, or two pages would overwrite
// each other's captures and share a scrape_runs attribution.
func TestTargetsAreDistinctAndPinnedToTheSeededPlatformIDs(t *testing.T) {
	seen := map[int64]string{}
	for _, target := range targets {
		if prev, dup := seen[target.platform]; dup {
			t.Errorf("platform %d is used by both %s and %s", target.platform, prev, target.source)
		}
		seen[target.platform] = target.source
		if target.source == "" {
			t.Errorf("index %s has an empty source name", target.index)
		}
		// targets hold paths; baseURL() supplies the host. The path must request wikitext, because
		// the parser cannot read the rendered article.
		if !strings.Contains(target.url, "List_of_S%26P") {
			t.Errorf("index %s path = %q, want the S%%26P constituent list", target.index, target.url)
		}
		if !strings.Contains(target.url, "action=raw") {
			t.Errorf("index %s path = %q, want action=raw: the parser reads wikitext, and the rendered article is HTML", target.index, target.url)
		}
	}
	if len(targets) != 3 {
		t.Errorf("targets has %d entries, want 3", len(targets))
	}
	// The ids are seeded by migration 004; a change here without a migration would point runs at
	// the wrong platform.
	for _, want := range []int64{20, 21, 22} {
		if _, ok := seen[want]; !ok {
			t.Errorf("platform id %d is not used by any target", want)
		}
	}
}

// TestRunEndToEndAgainstLocalPages drives the command's own wiring - config, migrations, HTTP client,
// all three targets - against a local server serving the recorded pages. No test here touches the
// network: the fixtures are the same bytes the parser's own tests use.
func TestRunEndToEndAgainstLocalPages(t *testing.T) {
	// The stub must serve the paths the command actually requests, or it would 404 and the test
	// would pass for the wrong reason - which is how the rendered-vs-raw URL bug survived this test.
	byIndex := map[string]string{"sp500": "sp500.wiki", "sp400": "sp400.wiki", "sp600": "sp600.wiki"}
	pages := map[string]string{}
	for _, target := range targets {
		pages[target.url] = byIndex[string(target.index)]
	}
	requested := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match on path plus query: the targets carry ?action=raw, and EscapedPath drops the query
		// entirely. The client percent-encodes "&" as %26, which EscapedPath preserves and
		// URL.Path has already decoded by the time the handler sees it.
		escaped := r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			escaped += "?" + r.URL.RawQuery
		}
		requested[escaped]++
		name, ok := pages[escaped]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(filepath.Join("..", "..", "internal", "sp1500", "testdata", name))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("DB_PATH", filepath.Join(dir, "jobs.db"))
	t.Setenv("DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("SP1500_BASE_URL", srv.URL)

	if code := run(); code != runindex.ExitOK {
		t.Fatalf("run() = %d, want %d", code, runindex.ExitOK)
	}

	// Every page was fetched exactly once, from the path the target declares.
	for path := range pages {
		if requested[path] != 1 {
			t.Errorf("path %s requested %d times, want 1", path, requested[path])
		}
	}

	database, err := db.Open(filepath.Join(dir, "jobs.db"))
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer func() { _ = database.Close() }()

	// 503 + 399 + 600 = 1502 rows, collapsing to fewer companies because dual-class listings
	// share a slug.
	var companies int
	if err := database.QueryRow(`SELECT count(*) FROM companies`).Scan(&companies); err != nil {
		t.Fatalf("count companies: %v", err)
	}
	if companies < 1480 || companies > 1502 {
		t.Errorf("companies = %d, want between 1480 and 1502", companies)
	}

	var runs, ok int
	if err := database.QueryRow(`SELECT count(*), sum(status = 'ok') FROM scrape_runs`).Scan(&runs, &ok); err != nil {
		t.Fatalf("count scrape_runs: %v", err)
	}
	if runs != 3 {
		t.Errorf("scrape_runs rows = %d, want 3", runs)
	}
	if ok != 3 {
		t.Errorf("%d of %d runs are ok, want all 3", ok, runs)
	}

	// All three index memberships are represented.
	var memberships int
	if err := database.QueryRow(
		`SELECT count(DISTINCT index_membership) FROM companies`).Scan(&memberships); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if memberships != 3 {
		t.Errorf("distinct index_membership values = %d, want 3", memberships)
	}

	// And a known company landed with its enrichment columns populated.
	var industry, hq string
	if err := database.QueryRow(
		`SELECT industry, headquarters_location FROM companies WHERE slug = 'apple-inc'`).Scan(&industry, &hq); err != nil {
		t.Fatalf("read Apple: %v", err)
	}
	if industry != "Information Technology" || hq != "Cupertino, California" {
		t.Errorf("Apple stored as %q / %q", industry, hq)
	}

	// Three raw captures, one per source, all for the same UTC day.
	entries, err := os.ReadDir(filepath.Join(dir, "data", "raw"))
	if err != nil {
		t.Fatalf("read raw dir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("raw captures = %d, want 3", len(entries))
	}
}
