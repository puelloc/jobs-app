// jobs.go: the generic job-trigger endpoints. POST /api/pipeline/{name} launches one of the
// one-off pipeline jobs (sp1500, classify, batch, scraper) inside the server's own container, and
// GET /api/pipeline/{id}/log streams its stdout back so the UI can monitor a non-agent job the same
// way it watches an agent trace.
package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"jobsapp/internal/store"
)

// jobSpec maps a trigger name to the platform its scrape_runs row records and the extra args the
// binary needs. The binary name is the trigger name itself (resolved on PATH, which in the image is
// /usr/local/bin).
type jobSpec struct {
	platform string
	args     []string
}

var triggerableJobs = map[string]jobSpec{
	"sp1500":   {platform: "career_bootstrap"},
	"classify": {platform: "career_classify", args: []string{"-commit"}},
	"batch":    {platform: "career_batch"},
	"scraper":  {platform: "remoteok"},
}

// JobResponse is the POST /api/pipeline/{name} response.
type JobResponse struct {
	RunID int64 `json:"run_id"`
}

// handleTriggerJob serves POST /api/jobs/{name}.
func handleTriggerJob(db *sql.DB, dataDir string, gate *jobGate) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		spec, ok := triggerableJobs[name]
		if !ok {
			writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("unknown job %q", name))
			return
		}

		if !gate.tryAcquire() {
			writeError(w, http.StatusConflict, codeConflict, "a job is already running; try again when it finishes")
			return
		}

		platformID, err := store.PlatformIDByName(r.Context(), db, spec.platform)
		if err != nil {
			gate.release()
			writeInternalError(w, fmt.Errorf("%s platform: %w", spec.platform, err))
			return
		}
		runID, _, err := store.StartRun(r.Context(), db, platformID)
		if err != nil {
			gate.release()
			writeInternalError(w, fmt.Errorf("start run: %w", err))
			return
		}

		logPath := filepath.Join(dataDir, "jobs", fmt.Sprintf("%d.log", runID))
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			gate.release()
			_ = store.FinishRun(r.Context(), db, runID, "error", 0, 0, 0, strPtr(fmt.Sprintf("log dir: %v", err)))
			writeInternalError(w, fmt.Errorf("create log dir: %w", err))
			return
		}
		logf, err := os.Create(logPath)
		if err != nil {
			gate.release()
			_ = store.FinishRun(r.Context(), db, runID, "error", 0, 0, 0, strPtr(fmt.Sprintf("log file: %v", err)))
			writeInternalError(w, fmt.Errorf("create log file: %w", err))
			return
		}

		cmd := exec.Command(name, spec.args...)
		cmd.Stdout = logf
		cmd.Stderr = logf
		if err := cmd.Start(); err != nil {
			_ = logf.Close()
			gate.release()
			_ = store.FinishRun(r.Context(), db, runID, "error", 0, 0, 0, strPtr(fmt.Sprintf("launch: %v", err)))
			writeInternalError(w, fmt.Errorf("launch %s: %w", name, err))
			return
		}

		// Reap the process, close the log, and finish the run in the background. The command's
		// own sub-runs (sp1500's per-index runs, batch's per-company scrapes) carry the real item
		// counts; this run records the job's exit status.
		go func() {
			waitErr := cmd.Wait()
			_ = logf.Close()
			status := "ok"
			var errText *string
			if waitErr != nil {
				status = "error"
				errText = strPtr(fmt.Sprintf("%v", waitErr))
			}
			_ = store.FinishRun(context.Background(), db, runID, status, 0, 0, 0, errText)
			gate.release()
		}()

		writeJSON(w, http.StatusAccepted, JobResponse{RunID: runID})
	}
}

// jobLogIDRE limits a log id to a single safe filename component.
var jobLogIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// strPtr returns a pointer to s, for the nullable error_text column.
func strPtr(s string) *string { return &s }

// JobLogResponse is the GET /api/jobs/{id}/log response: the command's stdout so far.
type JobLogResponse struct {
	ID      string `json:"id"`
	Present bool   `json:"present"`
	Log     string `json:"log"`
}

// handleGetJobLog serves GET /api/jobs/{id}/log.
func handleGetJobLog(dataDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !jobLogIDRE.MatchString(id) {
			writeError(w, http.StatusBadRequest, codeBadRequest, "log id must be a safe filename")
			return
		}
		path := filepath.Join(dataDir, "jobs", id+".log")
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusOK, JobLogResponse{ID: id, Present: false, Log: ""})
				return
			}
			writeInternalError(w, fmt.Errorf("read log %s: %w", id, err))
			return
		}
		writeJSON(w, http.StatusOK, JobLogResponse{ID: id, Present: true, Log: string(raw)})
	}
}
