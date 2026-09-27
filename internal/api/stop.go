// stop.go: the endpoint that cancels a running job. It marks the run cancelled and signals the
// in-flight job's whole process group, so the job's Python worker and Chromium die with it.
package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

	"jobsapp/internal/store"
)

// StopResponse is the POST /api/runs/{id}/stop response.
type StopResponse struct {
	RunID     int64 `json:"run_id"`
	Cancelled bool  `json:"cancelled"`
}

// handleStopRun serves POST /api/runs/{id}/stop.
func handleStopRun(db *sql.DB, runner *jobRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "run id must be an integer")
			return
		}

		// Mark first (guarded by status='running'), then signal. Order matters: stop() sets the
		// stopped flag before the process dies, so the reaper sees it and does not overwrite this
		// cancelled state with its own FinishRun.
		cancelled, err := store.CancelRun(r.Context(), db, id)
		if err != nil {
			writeInternalError(w, fmt.Errorf("cancel run %d: %w", id, err))
			return
		}
		if !cancelled {
			writeError(w, http.StatusConflict, codeConflict, fmt.Sprintf("run %d is not running", id))
			return
		}
		runner.stop()

		writeJSON(w, http.StatusOK, StopResponse{RunID: id, Cancelled: true})
	}
}
