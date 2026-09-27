// scrape.go: the handler behind POST /api/companies/{id}/scrape - the write path that lets the UI
// trigger one company's listings scrape.
//
// It is the first write endpoint on the otherwise read-only server, and it is deliberately narrow:
// look up the company and its classified vendor, open a scrape_runs row, and launch cmd/scrape in
// the background for that one company. The scrape itself owns finishing its run.
package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"jobsapp/internal/store"
)

// ScrapeResponse is the POST /api/companies/{id}/scrape response: the run id the UI polls via
// GET /api/runs (status) and GET /api/traces/{id} (live agent trace).
type ScrapeResponse struct {
	RunID int64 `json:"run_id"`
}

// handleScrapeCompany serves POST /api/companies/{id}/scrape.
func handleScrapeCompany(db *sql.DB, dataDir string, scrapeCmd []string, runner *jobRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(scrapeCmd) == 0 {
			writeError(w, http.StatusServiceUnavailable, codeInternal, "scrape command is not configured (SCRAPE_COMMAND)")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "company id must be an integer")
			return
		}

		slug, err := store.CompanySlugByID(r.Context(), db, id)
		if err != nil {
			if err == sql.ErrNoRows {
				writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("no company with id %d", id))
				return
			}
			writeInternalError(w, fmt.Errorf("company %d: %w", id, err))
			return
		}
		vendor, err := store.VendorNameForCompany(r.Context(), db, id)
		if err != nil {
			// Not classified yet: routing needs the vendor, so this is a conflict, not a 500.
			writeError(w, http.StatusBadRequest, codeBadRequest,
				fmt.Sprintf("company %s is not yet classified; run classify first", slug))
			return
		}

		if !runner.tryAcquire() {
			writeError(w, http.StatusConflict, codeConflict, "a job is already running; try again when it finishes")
			return
		}

		runPlatformID, err := store.PlatformIDByName(r.Context(), db, "career_listings")
		if err != nil {
			runner.release()
			writeInternalError(w, fmt.Errorf("career_listings platform: %w", err))
			return
		}
		runID, _, err := store.StartRun(r.Context(), db, runPlatformID)
		if err != nil {
			runner.release()
			writeInternalError(w, fmt.Errorf("start run: %w", err))
			return
		}

		// Capture the scrape's stdout to a log file (the same shape as pipeline jobs) so the run
		// page can show its summary alongside the agent trace.
		jobsDir := filepath.Join(dataDir, "jobs")
		if err := os.MkdirAll(jobsDir, 0o755); err != nil {
			runner.release()
			_ = store.FinishRun(r.Context(), db, runID, "error", 0, 0, 0, strPtr(fmt.Sprintf("log dir: %v", err)))
			writeInternalError(w, fmt.Errorf("create log dir: %w", err))
			return
		}
		logf, err := os.Create(filepath.Join(jobsDir, fmt.Sprintf("%d.log", runID)))
		if err != nil {
			runner.release()
			_ = store.FinishRun(r.Context(), db, runID, "error", 0, 0, 0, strPtr(fmt.Sprintf("log file: %v", err)))
			writeInternalError(w, fmt.Errorf("create log file: %w", err))
			return
		}

		// Fire-and-forget: cmd/scrape owns its run (it finishes it), so the request returns the id
		// immediately and a goroutine reaps the process when it exits, releasing the gate then.
		args := append([]string{}, scrapeCmd[1:]...)
		args = append(args, "--slug", slug, "--vendor", vendor, "--run-id", fmt.Sprintf("%d", runID))
		cmd := exec.Command(scrapeCmd[0], args...)
		cmd.Stdout = logf
		cmd.Stderr = logf
		// Own process group so a stop signals the scrape plus its Python/Chromium descendants.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			_ = logf.Close()
			runner.release()
			writeInternalError(w, fmt.Errorf("launch scrape: %w", err))
			return
		}
		runner.register(cmd)
		go func() {
			_ = cmd.Wait()
			_ = logf.Close()
			runner.release()
		}()

		writeJSON(w, http.StatusAccepted, ScrapeResponse{RunID: runID})
	}
}
