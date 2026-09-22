// integration_test.go: end-to-end tests for run(), the whole documented
// lifecycle, against a local fixture server.
//
// These drive the real config loader, the real database (migrations and all),
// the real HTTP client, and the real raw-file writer. Nothing here reaches the
// network. This is the only test that exercises run() itself, so it is what
// proves the exit codes and the wiring between steps.
package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureFeed mirrors the real capture's shape: a notice element with no id,
// followed by job elements. Kept tiny so the test is fast.
const fixtureFeed = `[
  {"last_updated": 1790087814, "legal": "attribution notice"},
  {"slug":"remote-go-engineer-acme-1137412","id":"1137412","epoch":1790006411,"date":"2026-09-21T16:00:11+00:00","company":"Ashby","company_logo":"","position":"Go Engineer","tags":["dev","golang"],"description":"<p>Hi! I\u00e2m Hannah</p>","location":"Remote","logo":"","url":"https://remoteOK.com/a","apply_url":"https://remoteOK.com/a","salary_min":30000,"salary_max":40000},
  {"slug":"remote-backend-engineer-acme-1137411","id":"1137411","epoch":1789862431,"date":"2026-09-20T00:00:31+00:00","company":"Delinea","company_logo":"","position":"Backend Engineer","tags":[],"description":"<p>plain</p>","location":"","logo":"","url":"https://remoteOK.com/b","apply_url":"https://remoteOK.com/b","salary_min":0,"salary_max":0}
]`

// newFixtureServer serves the fixture feed and counts requests.
func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixtureFeed))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// envForRun points the scraper at a fixture server and a temp workspace, and
// clears any variable that could leak in from the developer's shell.
func envForRun(t *testing.T, endpoint string) (dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	t.Setenv("DB_PATH", filepath.Join(dataDir, "jobs.db"))
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("REMOTEOK_ENDPOINT", endpoint)
	t.Setenv("USER_AGENT", "jobs-app-test/1")
	t.Setenv("HTTP_TIMEOUT", "10s")
	t.Setenv("LOG_LEVEL", "info")
	return dataDir
}

// design: the expected v1 outcome - zero accepted jobs because Normalize is
// stubbed, so exit 4 with a raw capture and an error run row.
func TestRunStubLifecycleEndToEnd(t *testing.T) {
	srv := newFixtureServer(t)
	dataDir := envForRun(t, srv.URL)

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = run(context.Background())
	})

	if code != exitNoJobs {
		t.Errorf("exit code = %d, want %d (zero accepted jobs)", code, exitNoJobs)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on a failed run", stdout)
	}
	// Exactly one stderr line, plus the per-element lines are debug-only.
	if n := strings.Count(stderr, "\n"); n != 1 {
		t.Errorf("stderr has %d lines, want exactly 1:\n%s", n, stderr)
	}
	for _, want := range []string{"status=error", "step=normalize", "condition=no_jobs", "exit=4"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}

	// The raw capture exists and holds the bytes verbatim.
	dbPath := filepath.Join(dataDir, "jobs.db")
	rawPath := filepath.Join(dataDir, "raw", rawFilenameNow(t, dbPath))
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("raw capture missing at %s: %v", rawPath, err)
	}
	if string(raw) != fixtureFeed {
		t.Errorf("raw capture was modified: %d bytes stored, want %d", len(raw), len(fixtureFeed))
	}

	// The run row is finished, not left in flight.
	database := openDBAt(t, dbPath)
	if n := countRows(t, database, "scrape_runs"); n != 1 {
		t.Errorf("scrape_runs rows = %d, want 1", n)
	}
	if n := countRows(t, database, "job_listings"); n != 0 {
		t.Errorf("job_listings rows = %d, want 0 while the normalizer is stubbed", n)
	}

	var status, finished, errText string
	var found int64
	if err := database.QueryRow(
		`SELECT status, finished_at, items_found, error_text FROM scrape_runs`).
		Scan(&status, &finished, &found, &errText); err != nil {
		t.Fatalf("read run row: %v", err)
	}
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if found != 3 {
		t.Errorf("items_found = %d, want 3 (all parsed elements)", found)
	}
	if errText == "" {
		t.Error("error_text is empty, want the condition and message")
	}
}

// design: Durability / "Same-day collision behavior" - exit 5, success line
// printed with raw=existing, counters still describing this run's writes.
func TestRunSecondSameDayRunReportsCollision(t *testing.T) {
	srv := newFixtureServer(t)
	dataDir := envForRun(t, srv.URL)

	var first int
	captureOutput(t, func() { first = run(context.Background()) })
	if first != exitNoJobs {
		t.Fatalf("first run exit = %d, want %d", first, exitNoJobs)
	}

	var second int
	stdout, _ := captureOutput(t, func() { second = run(context.Background()) })
	if second != exitRawCollision {
		t.Errorf("second run exit = %d, want %d on a same-day collision", second, exitRawCollision)
	}
	if !strings.Contains(stdout, "status=ok") {
		t.Errorf("stdout = %q, want the success line on exit 5", stdout)
	}
	if !strings.Contains(stdout, "raw=existing") {
		t.Errorf("stdout = %q, want raw=existing", stdout)
	}
	if !strings.Contains(stdout, "found=3") {
		t.Errorf("stdout = %q, want this run's counter (found=3)", stdout)
	}

	// Two finished runs, still zero job rows.
	database := openDBAt(t, filepath.Join(dataDir, "jobs.db"))
	if n := countRows(t, database, "scrape_runs"); n != 2 {
		t.Errorf("scrape_runs rows = %d, want 2", n)
	}
	if n := countRows(t, database, "job_listings"); n != 0 {
		t.Errorf("job_listings rows = %d, want 0", n)
	}
}

// design: Failure policy - a non-JSON body keeps its raw file and exits 1.
func TestRunParseFailureKeepsRawFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><html>not json</html>"))
	}))
	defer srv.Close()
	dataDir := envForRun(t, srv.URL)

	var code int
	_, stderr := captureOutput(t, func() { code = run(context.Background()) })

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d for an unparseable body", code, exitFailure)
	}
	if !strings.Contains(stderr, "step=parse") || !strings.Contains(stderr, "invalid_json") {
		t.Errorf("stderr = %q, want step=parse condition=invalid_json", stderr)
	}
	// The capture must survive a parse failure: re-parsing beats re-fetching.
	entries, err := os.ReadDir(filepath.Join(dataDir, "raw"))
	if err != nil {
		t.Fatalf("read raw dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("raw dir has %d files, want the unparseable capture kept", len(entries))
	}
}

// design: Failure policy - an HTTP error exits 1 without writing a raw file.
func TestRunHTTPErrorWritesNoRawFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	dataDir := envForRun(t, srv.URL)

	var code int
	stdout, stderr := captureOutput(t, func() { code = run(context.Background()) })

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "condition=http_403") {
		t.Errorf("stderr = %q, want condition=http_403", stderr)
	}
	if !strings.Contains(stderr, "http=403") {
		t.Errorf("stderr = %q, want http=403", stderr)
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "raw")); err == nil && len(entries) != 0 {
		t.Errorf("raw dir has %d files, want none after an HTTP error", len(entries))
	}
}

// design: Failure policy - a valid but empty array exits 4 and closes nothing.
func TestRunEmptyArrayExitsFourWithoutClosingJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	dataDir := envForRun(t, srv.URL)

	var code int
	_, stderr := captureOutput(t, func() { code = run(context.Background()) })

	if code != exitNoJobs {
		t.Errorf("exit code = %d, want %d", code, exitNoJobs)
	}
	if !strings.Contains(stderr, "condition=no_jobs") {
		t.Errorf("stderr = %q, want condition=no_jobs", stderr)
	}
	database := openDBAt(t, filepath.Join(dataDir, "jobs.db"))
	if n := countRows(t, database, "job_listings"); n != 0 {
		t.Errorf("job_listings rows = %d, want 0", n)
	}
}

// design: Run lifecycle step 1 - a bad env value exits 2 before any DB or network
// work, so no scrape_runs row and no raw file can exist.
func TestRunBadConfigExitsTwoWithoutTouchingAnything(t *testing.T) {
	srv := newFixtureServer(t)
	dataDir := envForRun(t, srv.URL)
	t.Setenv("HTTP_TIMEOUT", "not-a-duration")

	var code int
	stdout, stderr := captureOutput(t, func() { code = run(context.Background()) })

	if code != exitConfig {
		t.Errorf("exit code = %d, want %d", code, exitConfig)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "condition=bad_config") || !strings.Contains(stderr, "run=none") {
		t.Errorf("stderr = %q, want condition=bad_config and run=none", stderr)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "jobs.db")); !os.IsNotExist(err) {
		t.Error("a database was created despite a config error")
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "raw")); err == nil && len(entries) != 0 {
		t.Errorf("raw dir has %d files, want none after a config error", len(entries))
	}
}

// design: Failure policy - an unreachable endpoint exits 1 and writes no raw file.
func TestRunUnreachableEndpoint(t *testing.T) {
	srv := newFixtureServer(t)
	endpoint := srv.URL
	srv.Close() // nothing is listening now
	dataDir := envForRun(t, endpoint)

	var code int
	_, stderr := captureOutput(t, func() { code = run(context.Background()) })

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "step=fetch") {
		t.Errorf("stderr = %q, want step=fetch", stderr)
	}
	// The run row must still be finished rather than left 'running'.
	database := openDBAt(t, filepath.Join(dataDir, "jobs.db"))
	var status string
	if err := database.QueryRow(`SELECT status FROM scrape_runs`).Scan(&status); err != nil {
		t.Fatalf("read run row: %v", err)
	}
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
}

// design: Observability - per-element skip reasons are debug-only, so the
// default run emits exactly one stderr line.
func TestRunDebugLogLevelEmitsPerElementReasons(t *testing.T) {
	srv := newFixtureServer(t)
	envForRun(t, srv.URL)
	t.Setenv("LOG_LEVEL", "debug")

	var code int
	_, stderr := captureOutput(t, func() { code = run(context.Background()) })

	if code != exitNoJobs {
		t.Errorf("exit code = %d, want %d", code, exitNoJobs)
	}
	if n := strings.Count(stderr, "\n"); n != 4 { // 3 elements + the failure line
		t.Errorf("stderr has %d lines, want 4 (one per element plus the failure line):\n%s", n, stderr)
	}
	if !strings.Contains(stderr, "level=debug") || !strings.Contains(stderr, "skipped_index=0") {
		t.Errorf("stderr = %q, want per-element debug lines", stderr)
	}
	if !strings.Contains(stderr, "normalizer not implemented") {
		t.Errorf("stderr = %q, want the skip reason", stderr)
	}
}

// design: Failure policy - a raw-write failure is a startup/config failure: exit
// 2, and the run row records step=raw.
func TestRunRawWriteFailureExitsTwo(t *testing.T) {
	srv := newFixtureServer(t)
	dir := t.TempDir()

	// Point DATA_DIR at a regular file so the raw directory cannot be created.
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("create blocker file: %v", err)
	}
	t.Setenv("DB_PATH", filepath.Join(dir, "jobs.db"))
	t.Setenv("DATA_DIR", blocker)
	t.Setenv("REMOTEOK_ENDPOINT", srv.URL)
	t.Setenv("LOG_LEVEL", "info")

	var code int
	stdout, stderr := captureOutput(t, func() { code = run(context.Background()) })

	if code != exitConfig {
		t.Errorf("exit code = %d, want %d for an unwritable raw directory", code, exitConfig)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "step=raw") || !strings.Contains(stderr, "raw_write_failed") {
		t.Errorf("stderr = %q, want step=raw condition=raw_write_failed", stderr)
	}

	// The run row must still be finished by the terminal write.
	database := openDBAt(t, filepath.Join(dir, "jobs.db"))
	var status, errText string
	if err := database.QueryRow(`SELECT status, error_text FROM scrape_runs`).Scan(&status, &errText); err != nil {
		t.Fatalf("read run row: %v", err)
	}
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if !strings.Contains(errText, "raw_write_failed") {
		t.Errorf("error_text = %q, want it to record the raw-write failure", errText)
	}
}

// --- helpers -------------------------------------------------------------

// rawFilenameNow reads started_at back from the database and converts it to the
// capture filename, so the test does not have to guess the UTC date.
func rawFilenameNow(t *testing.T, dbPath string) string {
	t.Helper()
	database := openDBAt(t, dbPath)
	var startedAt string
	if err := database.QueryRow(`SELECT started_at FROM scrape_runs LIMIT 1`).Scan(&startedAt); err != nil {
		t.Fatalf("read started_at: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(dbPath), "raw"))
	if err != nil {
		t.Fatalf("read raw dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("raw dir has %d files, want exactly 1 (started_at=%s)", len(entries), startedAt)
	}
	return entries[0].Name()
}

// openDBAt opens an already-migrated database read-write for assertions.
func openDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func countRows(t *testing.T, database *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
