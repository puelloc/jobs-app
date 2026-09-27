// jobs_test.go: tests for the generic pipeline trigger and log endpoints.
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

func TestTriggerJob_RejectsConcurrentJob(t *testing.T) {
	writeFakeJob(t, "batch", "sleep 2")

	h, _, _ := newJobsTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/pipeline/batch")
	requireStatus(t, rec, http.StatusAccepted)

	rec2 := do(t, h, http.MethodPost, "/api/pipeline/batch")
	requireError(t, rec2, http.StatusConflict, "conflict")
}
