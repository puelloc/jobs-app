// runindex_test.go: the run lifecycle, driven by a stub HTTP client.
//
// No test here touches the network. The client is injected, so the whole fetch-parse-upsert path is
// exercised deterministically.
package runindex

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jobsapp/internal/db"
	"jobsapp/internal/sp1500"
)

// Two constituent rows in the S&P 500's own cell syntax, so the parser sees a shape it handles.
const twoRowPage = `{| class="wikitable"
|-
![[Ticker symbol|Symbol]]
! Security !! GICS Sector !! GICS Sub-Industry !! Headquarters Location !! Date added !! [[Central Index Key|CIK]] !! Founded
|-
|| {{NyseSymbol|AAA}}
|| [[Alpha Corporation]]
|| Industrials 
|| Industrial Conglomerates 
|| [[Boston]], Massachusetts 
|| 1957-03-04 
|| 0000066740
|| 1902
|-
|| {{NasdaqSymbol|BBB}}
|| [[Beta Inc.]]
|| Information Technology 
|| Semiconductors 
|| [[Austin, Texas]] 
|| 1999-10-12 
|| 0000006281
|| 1965
|}`

const emptyPage = `{| class="wikitable"
|-
! Symbol
! Security
|}`

type stubClient struct {
	body   string
	status int
	err    error
	calls  int
}

func (s *stubClient) Do(*http.Request) (*http.Response, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func newTestRunner(t *testing.T, client Client) (Runner, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// A fixed clock that stays inside one UTC day, so the capture filename is deterministic.
	now := func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }
	return Runner{
		DB:       database,
		Client:   client,
		DataDir:  dir,
		URL:      "https://example.test/wiki",
		Index:    sp1500.SP500,
		Source:   "wikipedia_sp500",
		Platform: 20, // wikipedia_sp500, seeded by migration 004
		Now:      now,
	}, database
}

func TestRunPersistsParsedCompaniesAndRecordsTheRun(t *testing.T) {
	runner, database := newTestRunner(t, &stubClient{body: twoRowPage})

	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != ExitOK {
		t.Errorf("ExitCode = %d, want %d", res.ExitCode, ExitOK)
	}
	if res.Found != 2 {
		t.Errorf("Found = %d, want 2", res.Found)
	}
	if res.Inserted != 2 {
		t.Errorf("Inserted = %d, want 2", res.Inserted)
	}
	if res.RawState != "written" {
		t.Errorf("RawState = %q, want written", res.RawState)
	}

	var name, industry, hq string
	if err := database.QueryRow(
		`SELECT name, industry, headquarters_location FROM companies WHERE slug = 'alpha-corporation'`).
		Scan(&name, &industry, &hq); err != nil {
		t.Fatalf("read company: %v", err)
	}
	if name != "Alpha Corporation" || industry != "Industrials" || hq != "Boston, Massachusetts" {
		t.Errorf("stored %q / %q / %q, want the parsed values", name, industry, hq)
	}

	// The run row must exist and be finished.
	var status string
	var found, inserted int
	var finished sql.NullString
	if err := database.QueryRow(
		`SELECT status, items_found, items_inserted, finished_at FROM scrape_runs WHERE id = ?`, res.RunID).
		Scan(&status, &found, &inserted, &finished); err != nil {
		t.Fatalf("read scrape_runs: %v", err)
	}
	if status != "ok" {
		t.Errorf("scrape_runs.status = %q, want ok", status)
	}
	if found != 2 || inserted != 2 {
		t.Errorf("counters = %d/%d, want 2/2", found, inserted)
	}
	if !finished.Valid {
		t.Error("finished_at is NULL after a successful run")
	}
}

// The whole point of writing raw before parsing is that a parse failure still leaves the payload.
func TestRunKeepsRawPayloadWhenParsingFails(t *testing.T) {
	// An unclosed table: the parser refuses it.
	runner, database := newTestRunner(t, &stubClient{body: `{| class="wikitable"
|-
|| {{NyseSymbol|AAA}}
`})

	res, err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded on an unparseable page, want an error")
	}
	if res.ExitCode != ExitFailure {
		t.Errorf("ExitCode = %d, want %d", res.ExitCode, ExitFailure)
	}
	if res.RawPath == "" {
		t.Fatal("RawPath is empty: the payload was not kept for re-parsing")
	}

	body, readErr := readFile(res.RawPath)
	if readErr != nil {
		t.Fatalf("read raw payload: %v", readErr)
	}
	if !strings.Contains(body, "NyseSymbol|AAA") {
		t.Errorf("raw payload does not hold the response body: %q", body)
	}

	var status string
	if err := database.QueryRow(`SELECT status FROM scrape_runs WHERE id = ?`, res.RunID).Scan(&status); err != nil {
		t.Fatalf("read scrape_runs: %v", err)
	}
	if status != "error" {
		t.Errorf("scrape_runs.status = %q, want error", status)
	}
}

func TestRunFailsOnANonSuccessStatus(t *testing.T) {
	runner, database := newTestRunner(t, &stubClient{status: http.StatusInternalServerError, body: "boom"})

	res, err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded on a 500, want an error")
	}
	if res.ExitCode != ExitFailure {
		t.Errorf("ExitCode = %d, want %d", res.ExitCode, ExitFailure)
	}
	if res.RawPath != "" {
		t.Errorf("RawPath = %q, want empty: an error page must not be stored as a capture", res.RawPath)
	}

	var status string
	if err := database.QueryRow(`SELECT status FROM scrape_runs WHERE id = ?`, res.RunID).Scan(&status); err != nil {
		t.Fatalf("read scrape_runs: %v", err)
	}
	if status != "error" {
		t.Errorf("scrape_runs.status = %q, want error", status)
	}
}

func TestRunFailsOnATransportError(t *testing.T) {
	runner, _ := newTestRunner(t, &stubClient{err: io.ErrUnexpectedEOF})

	res, err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded on a transport error, want an error")
	}
	if res.ExitCode != ExitFailure {
		t.Errorf("ExitCode = %d, want %d", res.ExitCode, ExitFailure)
	}
}

// A page that parses to zero rows is exit 4, not a quiet success.
func TestRunReportsEmptyPageAsExitFour(t *testing.T) {
	runner, _ := newTestRunner(t, &stubClient{body: emptyPage})

	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Found != 0 {
		t.Errorf("Found = %d, want 0", res.Found)
	}
	if res.ExitCode != ExitEmpty {
		t.Errorf("ExitCode = %d, want %d", res.ExitCode, ExitEmpty)
	}
}

// The first run of a UTC day owns the capture; a second still processes the data but exits 5.
func TestRunReportsSameDayCaptureAsExitFive(t *testing.T) {
	runner, database := newTestRunner(t, &stubClient{body: twoRowPage})

	first, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.ExitCode != ExitOK {
		t.Fatalf("first ExitCode = %d, want %d", first.ExitCode, ExitOK)
	}

	second, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.ExitCode != ExitRawCollision {
		t.Errorf("second ExitCode = %d, want %d", second.ExitCode, ExitRawCollision)
	}
	if second.RawState != "existing" {
		t.Errorf("second RawState = %q, want existing", second.RawState)
	}
	if second.RawPath != first.RawPath {
		t.Errorf("second RawPath = %q, want the first run's %q", second.RawPath, first.RawPath)
	}
	// The second run still did its database work: the collision is not a failure.
	if second.Inserted != 0 {
		t.Errorf("second Inserted = %d, want 0 (already present)", second.Inserted)
	}

	var n int
	if err := database.QueryRow(`SELECT count(*) FROM companies`).Scan(&n); err != nil {
		t.Fatalf("count companies: %v", err)
	}
	if n != 2 {
		t.Errorf("companies rows = %d, want 2 after two runs", n)
	}

	var runs int
	if err := database.QueryRow(`SELECT count(*) FROM scrape_runs`).Scan(&runs); err != nil {
		t.Fatalf("count scrape_runs: %v", err)
	}
	if runs != 2 {
		t.Errorf("scrape_runs rows = %d, want 2: every run is recorded", runs)
	}
}

// requestURLCheck proves the runner asks for the configured URL.
func TestRunRequestsTheConfiguredURL(t *testing.T) {
	stub := &stubClient{body: twoRowPage}
	runner, _ := newTestRunner(t, stub)
	runner.URL = "https://example.test/specific-page"

	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("client called %d times, want 1", stub.calls)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
