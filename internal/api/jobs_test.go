// jobs_test.go: tests for the generic pipeline trigger and log endpoints.
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"jobsapp/internal/db"
)

// writeFakeJob creates an executable shell script named `name` in a temp dir and prepends that dir
// to PATH, so handleTriggerJob's exec.Command(name) resolves to it instead of a real binary.
func writeFakeJob(t *testing.T, name, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// newJobsTestServer returns a router wired to a real in-memory DB plus the data dir the handler
// writes logs into.
func newJobsTestServer(t *testing.T) (http.Handler, *sql.DB, string) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dataDir := t.TempDir()
	return NewRouter(database, dataDir, []string{"true"}), database, dataDir
}

// waitForRun polls until the run reaches a terminal status (or the deadline passes).
func waitForRun(t *testing.T, database *sql.DB, runID int64) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var status string
		if err := database.QueryRow(`SELECT status FROM scrape_runs WHERE id = ?`, runID).Scan(&status); err != nil {
			t.Fatalf("read run %d: %v", runID, err)
		}
		if status != "running" {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d never finished", runID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTriggerJob_UnknownNameIs404(t *testing.T) {
	h, _, _ := newJobsTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/pipeline/bogus")
	requireError(t, rec, http.StatusNotFound, "not_found")
}

func TestTriggerJob_StartsRunAndCapturesLog(t *testing.T) {
	// classify is server-tracked (it does not create its own run), so the server records the run
	// and captures stdout to a per-run log file.
	writeFakeJob(t, "classify", "echo fake-classify-ran")

	h, database, dataDir := newJobsTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/pipeline/classify")
	requireStatus(t, rec, http.StatusAccepted)

	var got JobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID == 0 {
		t.Fatal("run_id should be non-zero")
	}
	if status := waitForRun(t, database, got.RunID); status != "ok" {
		t.Errorf("run status = %q, want ok", status)
	}

	// The command's stdout lands in <dataDir>/jobs/<run_id>.log.
	logPath := filepath.Join(dataDir, "jobs", strconv.FormatInt(got.RunID, 10)+".log")
	if b, err := os.ReadFile(logPath); err != nil {
		t.Fatalf("read log file: %v", err)
	} else if len(b) == 0 {
		t.Error("log file is empty; want the fake command's output")
	}

	// And it is served back by the log endpoint.
	logRec := do(t, h, http.MethodGet, "/api/pipeline/"+strconv.FormatInt(got.RunID, 10)+"/log")
	requireStatus(t, logRec, http.StatusOK)
	var log JobLogResponse
	if err := json.Unmarshal(logRec.Body.Bytes(), &log); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if !log.Present {
		t.Fatal("log should be present")
	}
	if log.Log == "" {
		t.Error("log is empty; want the fake command's output")
	}
}

func TestTriggerJob_SelfTrackingReturnsZeroRunID(t *testing.T) {
	// sp1500 (indices) creates its own runs, so the server must not create one too.
	writeFakeJob(t, "sp1500", "echo fake-sp1500-ran")

	h, _, _ := newJobsTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/pipeline/sp1500")
	requireStatus(t, rec, http.StatusAccepted)

	var got JobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != 0 {
		t.Errorf("run_id = %d, want 0 (self-tracking job)", got.RunID)
	}
}

func TestTriggerJob_LogMissingIsPresentFalse(t *testing.T) {
	h, _, _ := newJobsTestServer(t)
	rec := do(t, h, http.MethodGet, "/api/pipeline/999/log")
	requireStatus(t, rec, http.StatusOK)
	var log JobLogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &log); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if log.Present {
		t.Error("a missing log should report present=false")
	}
}

func TestBatchArgsFromBody_Full(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/pipeline/batch",
		strings.NewReader(`{"skip_ok":true,"skip_traced":true,"from_slug":"abbott-laboratories","stop_after_failures":3,"limit":10}`))
	args, err := batchArgsFromBody(req)
	if err != nil {
		t.Fatalf("batchArgsFromBody: %v", err)
	}
	want := []string{
		"-skip-ok", "-skip-traced", "-from-slug", "abbott-laboratories",
		"-stop-after-failures", "3", "-limit", "10",
	}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestBatchArgsFromBody_EmptyBodyIsNoArgs(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/pipeline/batch", nil)
	args, err := batchArgsFromBody(req)
	if err != nil {
		t.Fatalf("batchArgsFromBody: %v", err)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want none", args)
	}
}

func TestBatchArgsFromBody_Malformed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/pipeline/batch", strings.NewReader(`{not json`))
	if _, err := batchArgsFromBody(req); err == nil {
		t.Fatal("batchArgsFromBody: want an error for malformed JSON")
	}
}

func TestTriggerJob_BatchForwardsOptions(t *testing.T) {
	// A fake `batch` binary that writes its argv to a file, so the test can assert the flags the
	// server derived from the request body actually reached the command line.
	outPath := filepath.Join(t.TempDir(), "args.txt")
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + outPath + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "batch"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake batch: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	h, _, _ := newJobsTestServer(t)
	body := `{"skip_ok":true,"from_slug":"abbott-laboratories","stop_after_failures":3}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pipeline/batch", strings.NewReader(body)))
	requireStatus(t, rec, http.StatusAccepted)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if b, err := os.ReadFile(outPath); err == nil {
			got := string(b)
			if strings.Contains(got, "-skip-ok\n") &&
				strings.Contains(got, "-from-slug\nabbott-laboratories\n") &&
				strings.Contains(got, "-stop-after-failures\n3\n") &&
				!strings.Contains(got, "-skip-traced") {
				return
			}
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(outPath)
			t.Fatalf("batch args not captured; got %q", string(b))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTriggerJob_RejectsConcurrentJob(t *testing.T) {
	writeFakeJob(t, "batch", "sleep 2")

	h, _, _ := newJobsTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/pipeline/batch")
	requireStatus(t, rec, http.StatusAccepted)

	rec2 := do(t, h, http.MethodPost, "/api/pipeline/batch")
	requireError(t, rec2, http.StatusConflict, "conflict")
}

func TestStopRun_CancelsRunningJob(t *testing.T) {
	writeFakeJob(t, "batch", "sleep 5")

	h, database, _ := newJobsTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/pipeline/batch")
	requireStatus(t, rec, http.StatusAccepted)

	var got JobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	stop := do(t, h, http.MethodPost, "/api/runs/"+strconv.FormatInt(got.RunID, 10)+"/stop")
	requireStatus(t, stop, http.StatusOK)

	var status string
	var errText sql.NullString
	if err := database.QueryRow(`SELECT status, error_text FROM scrape_runs WHERE id = ?`, got.RunID).
		Scan(&status, &errText); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if !errText.Valid || errText.String != "cancelled by user" {
		t.Errorf("error_text = %v, want 'cancelled by user'", errText)
	}
}

func TestStopRun_NotRunningIs409(t *testing.T) {
	h, _, _ := newJobsTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/runs/999/stop")
	requireError(t, rec, http.StatusConflict, "conflict")
}
