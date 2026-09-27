// validate_cli_test.go: tests for the validate subcommand's argument handling, containment and
// exit mapping.
//
// The exit mapping is the part worth testing hardest, because three different outcomes all report
// zero confirmed:
//
//	failed>0      -> 1   an error, whatever else happened
//	confirmed>0   -> 0
//	neither       -> 4   a genuine "considered companies and confirmed none"
//
// The containment is the other half: the tier is opt-in, and a run that is not enabled must leave
// no trace at all - not even a scrape_runs row.
package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jobsapp/internal/db"
	"jobsapp/internal/runvalidate"
)

// --- flag parsing ---------------------------------------------------------

func TestValidateOnlySlugsEmptyIsConfigError(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseValidateFlags([]string{"--only-slugs="}, &stderr); err == nil {
		t.Fatal("an explicitly empty --only-slugs was accepted")
	}
	if _, err := parseValidateFlags([]string{"--only-slugs= , ,"}, &stderr); err == nil {
		t.Fatal("a whitespace-only --only-slugs was accepted")
	}
	got, err := parseValidateFlags(nil, &stderr)
	if err != nil {
		t.Fatalf("no --only-slugs should be valid: %v", err)
	}
	if len(got.slugs()) != 0 {
		t.Errorf("slugs() = %v, want empty", got.slugs())
	}
}

func TestValidateFlagsRejectValuesThatAreWrongRatherThanAbsent(t *testing.T) {
	var stderr bytes.Buffer
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"negative limit", []string{"--limit=-1"}},
		{"zero concurrency", []string{"--concurrency=0"}},
		{"negative concurrency", []string{"--concurrency=-2"}},
		{"zero page timeout", []string{"--page-timeout=0s"}},
		{"negative worker timeout", []string{"--worker-timeout=-1s"}},
		{"zero agent timeout", []string{"--agent-timeout=0s"}},
		{"zero agent steps", []string{"--agent-max-steps=0"}},
		{"zero body bound", []string{"--max-body-bytes=0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseValidateFlags(tc.args, &stderr); err == nil {
				t.Errorf("%v was accepted, want a config error", tc.args)
			}
		})
	}
}

func TestValidateFlagsParseEveryOption(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseValidateFlags([]string{
		"--only-slugs= acme , nike-inc,,", "--limit=10", "--concurrency=3", "--dry-run", "--escalate",
		"--page-timeout=20s", "--worker-timeout=40s", "--agent-timeout=2m", "--agent-max-steps=5",
		"--max-body-bytes=2048",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseValidateFlags: %v", err)
	}
	if strings.Join(got.slugs(), "|") != "acme|nike-inc" {
		t.Errorf("slugs() = %v", got.slugs())
	}
	if got.limit != 10 || got.concurrency != 3 || !got.dryRun || !got.escalate {
		t.Errorf("flags = %+v", got)
	}
	if got.pageTimeout.Seconds() != 20 || got.workerTimeout.Seconds() != 40 || got.agentTimeout.Seconds() != 120 {
		t.Errorf("timeouts = %+v", got)
	}
	if got.agentSteps != 5 || got.maxBodyBytes != 2048 {
		t.Errorf("agentSteps = %d, maxBodyBytes = %d", got.agentSteps, got.maxBodyBytes)
	}
}

// --- exit mapping ---------------------------------------------------------

func TestValidateExitZeroRequiresNoFailedItems(t *testing.T) {
	if got := validateExitCode(runvalidate.Summary{Confirmed: 500}); got != exitOK {
		t.Errorf("exit = %d, want %d", got, exitOK)
	}
	// One failure is a failure even when hundreds confirmed: a run that cannot say "one company
	// errored" is not reporting honestly.
	if got := validateExitCode(runvalidate.Summary{Confirmed: 500, Failed: 1}); got != exitFailure {
		t.Errorf("exit = %d, want %d", got, exitFailure)
	}
}

func TestValidateExitFourIsDistinctFromExitOne(t *testing.T) {
	if got := validateExitCode(runvalidate.Summary{Found: 12}); got != exitEmpty {
		t.Errorf("exit = %d, want %d for a considered-nothing-confirmed run", got, exitEmpty)
	}
	if got := validateExitCode(runvalidate.Summary{Found: 12, Failed: 12}); got != exitFailure {
		t.Errorf("exit = %d, want %d when every company errored", got, exitFailure)
	}
	// Found == 0 and no failures is the same zero-result outcome.
	if got := validateExitCode(runvalidate.Summary{}); got != exitEmpty {
		t.Errorf("exit = %d, want %d", got, exitEmpty)
	}
}

func TestValidateStdoutLineCarriesTheThreeWaySplit(t *testing.T) {
	line := formatValidateLine(runvalidate.Summary{
		RunID: 7, Found: 593, Confirmed: 480, Wrong: 61, Unverifiable: 52, Escalated: 40,
	}, true, 41)
	for _, want := range []string{
		"run_id=7", "dry_run=1", "companies=593", "skipped=41", "confirmed=480", "wrong=61",
		"unverifiable=52", "escalated=40", "failed=0",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %q", line, want)
		}
	}
	if strings.Contains(line, "\n") {
		t.Errorf("line contains a newline: %q", line)
	}
}

// --- containment ----------------------------------------------------------

// A run that is not enabled must leave no trace. Opening the database would apply migrations and a
// start-the-run-first design would leave a 'running' row, so the flag is checked before either.
func TestValidateRefusesToRunWithoutTheFeatureFlag(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "jobs.db")
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ENABLE_BROWSER_USE", "")
	t.Setenv("BROWSER_WORKER_COMMAND", "/bin/true")

	var stdout, stderr bytes.Buffer
	code := runValidate(nil, &stdout, &stderr)

	if code != exitConfig {
		t.Fatalf("exit = %d, want %d", code, exitConfig)
	}
	if !strings.Contains(stderr.String(), "ENABLE_BROWSER_USE") {
		t.Errorf("stderr = %q, want it to name the flag", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing on a refused run", stdout.String())
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("the database was created despite the tier being disabled (stat err = %v)", err)
	}
}

// Escalation without a model host would fail for every company for a reason nothing in the run
// explains, so it is refused up front rather than discovered 600 times.
func TestValidateRefusesEscalationWithoutAModelHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DB_PATH", filepath.Join(dir, "jobs.db"))
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ENABLE_BROWSER_USE", "1")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("BROWSER_WORKER_COMMAND", "/bin/true")

	var stdout, stderr bytes.Buffer
	code := runValidate([]string{"--escalate"}, &stdout, &stderr)

	if code != exitConfig {
		t.Fatalf("exit = %d, want %d", code, exitConfig)
	}
	if !strings.Contains(stderr.String(), "OLLAMA_HOST") {
		t.Errorf("stderr = %q, want it to name OLLAMA_HOST", stderr.String())
	}
}

// --- end to end, with a fake worker ---------------------------------------

// fakeWorkerScript writes a shell stand-in for the Python worker. The command is whitespace-split
// and shell-free precisely so this substitution is a single environment variable.
func fakeWorkerScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake_worker.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake worker: %v", err)
	}
	return "/bin/sh " + path
}

// seedValidationDB creates a migrated database with one company whose stored URL is a careers page
// and one whose stored URL is a product page.
func seedValidationDB(t *testing.T, dir string) string {
	t.Helper()
	dbPath := filepath.Join(dir, "jobs.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	defer func() { _ = database.Close() }()

	for _, c := range []struct{ slug, name, url string }{
		{"example-corp", "Example Corp", "https://example.com/careers"},
		{"widget-co", "Widget Co", "https://example.com/products"},
	} {
		if _, err := database.Exec(`
INSERT INTO companies (slug, name, career_site_url, career_site_url_source) VALUES (?, ?, ?, 'nav_anchor')`,
			c.slug, c.name, c.url); err != nil {
			t.Fatalf("seed %s: %v", c.slug, err)
		}
	}
	return dbPath
}

func TestValidateEndToEndWithAFakeWorker(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedValidationDB(t, dir)
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ENABLE_BROWSER_USE", "1")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("BROWSER_WORKER_COMMAND", fakeWorkerScript(t, `
req=$(cat)
case "$req" in
  *"/products"*)
    printf '%s' '{"ok":true,"mode":"render","final_url":"https://example.com/products","status":200,"content_type":"text/html","title":"Our Products","body":"<html><title>Our Products</title><p>buy things</p></html>"}'
    ;;
  *)
    printf '%s' '{"ok":true,"mode":"render","final_url":"https://example.com/careers","status":200,"content_type":"text/html","title":"Careers | Example Corp","body":"<html><title>Careers | Example Corp</title><h1>Careers</h1></html>"}'
    ;;
esac
`))

	var stdout, stderr bytes.Buffer
	code := runValidate(nil, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}
	line := stdout.String()
	for _, want := range []string{"companies=2", "confirmed=1", "wrong=1", "unverifiable=0", "failed=0"} {
		if !strings.Contains(line, want) {
			t.Errorf("stdout %q is missing %q", line, want)
		}
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer func() { _ = database.Close() }()

	var attempts int
	if err := database.QueryRow(`SELECT count(*) FROM url_resolution_attempts`).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}

	var source string
	if err := database.QueryRow(`SELECT source FROM url_resolution_attempts LIMIT 1`).Scan(&source); err != nil {
		t.Fatalf("read attempt source: %v", err)
	}
	if source != "browser_use" {
		t.Errorf("attempt source = %q, want browser_use", source)
	}

	// The stored URLs are the thing being measured, so they must be exactly what they were.
	var url string
	var checkedAt sql.NullString
	if err := database.QueryRow(`SELECT career_site_url, career_site_url_checked_at FROM companies WHERE slug = 'widget-co'`).
		Scan(&url, &checkedAt); err != nil {
		t.Fatalf("read widget-co: %v", err)
	}
	if url != "https://example.com/products" {
		t.Errorf("career_site_url = %q, want it untouched", url)
	}
	if !checkedAt.Valid {
		t.Error("checked_at was not stamped")
	}
}

// A worker that breaks protocol is a run failure, and the stderr line has to name it rather than
// reporting a quiet zero.
func TestValidateReportsAWorkerThatBreaksProtocol(t *testing.T) {
	dir := t.TempDir()
	seedValidationDB(t, dir)
	t.Setenv("DB_PATH", filepath.Join(dir, "jobs.db"))
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ENABLE_BROWSER_USE", "1")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("BROWSER_WORKER_COMMAND", fakeWorkerScript(t, `
cat >/dev/null
echo 'not json at all'
`))

	var stdout, stderr bytes.Buffer
	code := runValidate(nil, &stdout, &stderr)

	if code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing on a failed run", stdout.String())
	}
	if !strings.Contains(stderr.String(), "failed=2") {
		t.Errorf("stderr = %q, want the failure count", stderr.String())
	}
}

// The fake-worker seam means the command never needs a browser in tests; this asserts that the
// pipeline is wired to the configured command rather than to the default interpreter.
func TestValidateUsesTheConfiguredWorkerCommand(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "jobs.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := database.Exec(`
INSERT INTO companies (slug, name, career_site_url) VALUES ('example-corp','Example Corp','https://example.com/careers')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = database.Close()

	marker := filepath.Join(dir, "worker-ran")
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ENABLE_BROWSER_USE", "1")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("BROWSER_WORKER_COMMAND", fakeWorkerScript(t, `
cat >/dev/null
touch "`+marker+`"
printf '%s' '{"ok":true,"mode":"render","final_url":"https://example.com/careers","status":200,"content_type":"text/html","title":"Careers | Example Corp","body":"<html><title>Careers | Example Corp</title></html>"}'
`))

	var stdout, stderr bytes.Buffer
	if code := runValidate(nil, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the configured worker command did not run: %v", err)
	}
}

// --- selection filters ----------------------------------------------------

func TestValidateFlagsParseTheSelectionFilters(t *testing.T) {
	var stderr bytes.Buffer

	// No selection flag at all is the default skip-validated rule, not "everything".
	def, err := parseValidateFlags(nil, &stderr)
	if err != nil {
		t.Fatalf("parseValidateFlags(): %v", err)
	}
	if def.refresh || def.staleAfter != 0 {
		t.Errorf("default flags = %+v, want neither --refresh nor --stale-after", def)
	}
	if got := selectionFilter(def, time.Now()); !got.SkipValidated || got.VerdictCutoff != "" {
		t.Errorf("default filter = %+v, want PendingOnly", got)
	}

	refresh, err := parseValidateFlags([]string{"--refresh"}, &stderr)
	if err != nil {
		t.Fatalf("parseValidateFlags(--refresh): %v", err)
	}
	if got := selectionFilter(refresh, time.Now()); got.SkipValidated || got.VerdictCutoff != "" {
		t.Errorf("--refresh filter = %+v, want no verdict restriction at all", got)
	}

	stale, err := parseValidateFlags([]string{"--stale-after=24h"}, &stderr)
	if err != nil {
		t.Fatalf("parseValidateFlags(--stale-after): %v", err)
	}
	if got := selectionFilter(stale, time.Now()); got.SkipValidated || got.VerdictCutoff == "" {
		t.Errorf("--stale-after filter = %+v, want an age cutoff", got)
	}
}

// "everything" and "the older ones" are alternatives, so asking for both is a contradiction rather
// than a narrower union.
func TestValidateRefreshAndStaleAfterContradict(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseValidateFlags([]string{"--refresh", "--stale-after=1h"}, &stderr); err == nil {
		t.Fatal("--refresh with --stale-after was accepted")
	}
}

func TestValidateRejectsNegativeStaleAfter(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseValidateFlags([]string{"--stale-after=-1h"}, &stderr); err == nil {
		t.Fatal("a negative --stale-after was accepted; it would select nothing and look like a clean run")
	}
}

// The default behaviour the objective asks for: a sweep run twice must not re-fetch what it already
// validated. The second run therefore finds nothing to do and says how many it skipped.
func TestValidateSkipsAlreadyValidatedCompaniesByDefault(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedValidationDB(t, dir)
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ENABLE_BROWSER_USE", "1")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("BROWSER_WORKER_COMMAND", fakeWorkerScript(t, `
cat >/dev/null
printf '%s' '{"ok":true,"mode":"render","final_url":"https://example.com/careers","status":200,"content_type":"text/html","title":"Careers | Example Corp","body":"<html><title>Careers | Example Corp</title><h1>Careers</h1></html>"}'
`))

	var stdout, stderr bytes.Buffer
	if code := runValidate(nil, &stdout, &stderr); code != exitOK {
		t.Fatalf("first run exit = %d (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "companies=2") || !strings.Contains(stdout.String(), "skipped=0") {
		t.Errorf("first run line = %q, want companies=2 skipped=0", stdout.String())
	}

	// Both companies now carry a verdict, so the default (skip-validated) run has nothing left to do.
	stdout.Reset()
	stderr.Reset()
	code := runValidate(nil, &stdout, &stderr)
	if code != exitEmpty {
		t.Fatalf("second run exit = %d, want %d (stderr: %s)", code, exitEmpty, stderr.String())
	}
	if !strings.Contains(stdout.String(), "companies=0") || !strings.Contains(stdout.String(), "skipped=2") {
		t.Errorf("second run line = %q, want companies=0 skipped=2", stdout.String())
	}

	// --refresh is the override: it validates them again.
	stdout.Reset()
	stderr.Reset()
	if code := runValidate([]string{"--refresh"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("--refresh exit = %d (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "companies=2") || !strings.Contains(stdout.String(), "skipped=0") {
		t.Errorf("--refresh line = %q, want companies=2 skipped=0", stdout.String())
	}
}
