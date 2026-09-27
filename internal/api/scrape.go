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
	"os/exec"
	"strconv"
	"sync"

	"jobsapp/internal/store"
)

// ScrapeResponse is the POST /api/companies/{id}/scrape response: the run id the UI polls via
// GET /api/runs (status) and GET /api/traces/{id} (live agent trace).
type ScrapeResponse struct {
	RunID int64 `json:"run_id"`
}

// scrapeGate serializes scrapes within one server process: only one listing scrape runs at a time.
// The full sweep goes through cmd/batch, which is sequential by construction; this is the same
// boundary for the per-company trigger, so clicking "scrape" on several companies cannot fan out
// into concurrent browser agents (which would contend on SQLite's single writer and the shared
// model host). A process that is already in flight makes a new trigger a 409 conflict.
type scrapeGate struct {
	mu      sync.Mutex
	running bool
}

func (g *scrapeGate) tryAcquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return false
	}
	g.running = true
	return true
}

func (g *scrapeGate) release() {
	g.mu.Lock()
	g.running = false
	g.mu.Unlock()
}

// handleScrapeCompany serves POST /api/companies/{id}/scrape.
func handleScrapeCompany(db *sql.DB, scrapeCmd []string, gate *scrapeGate) http.HandlerFunc {
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

		if !gate.tryAcquire() {
			writeError(w, http.StatusConflict, codeConflict, "a scrape is already running; try again when it finishes")
			return
		}

		runPlatformID, err := store.PlatformIDByName(r.Context(), db, "career_listings")
		if err != nil {
			gate.release()
			writeInternalError(w, fmt.Errorf("career_listings platform: %w", err))
			return
		}
		runID, _, err := store.StartRun(r.Context(), db, runPlatformID)
		if err != nil {
			gate.release()
			writeInternalError(w, fmt.Errorf("start run: %w", err))
			return
		}

		// Fire-and-forget: cmd/scrape owns its run (it finishes it), so the request returns the id
		// immediately and a goroutine reaps the process when it exits, releasing the gate then.
		args := append([]string{}, scrapeCmd[1:]...)
		args = append(args, "--slug", slug, "--vendor", vendor, "--run-id", fmt.Sprintf("%d", runID))
		cmd := exec.Command(scrapeCmd[0], args...)
		if err := cmd.Start(); err != nil {
			gate.release()
			writeInternalError(w, fmt.Errorf("launch scrape: %w", err))
			return
		}
		go func() {
			_ = cmd.Wait()
			gate.release()
		}()

		writeJSON(w, http.StatusAccepted, ScrapeResponse{RunID: runID})
	}
}
