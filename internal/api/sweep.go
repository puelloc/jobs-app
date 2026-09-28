// sweep.go: the read endpoint for the full-sweep resume point. cmd/batch leaves a one-token file at
// <dataDir>/sweep/resume holding the slug a resume should start from; this endpoint serves it so the
// Runs dashboard can offer a one-click "Resume sweep" instead of asking the operator to read a log.
package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// SweepPositionResponse is the GET /api/sweep/position response. Present is false when there is no
// saved resume point (no sweep has stopped, or the last one ran to completion).
type SweepPositionResponse struct {
	Present bool   `json:"present"`
	Slug    string `json:"slug"`
}

// handleGetSweepPosition serves GET /api/sweep/position.
func handleGetSweepPosition(dataDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile(filepath.Join(dataDir, "sweep", "resume"))
		if err != nil {
			// No resume point (or the file cannot be read): report not present, never a 500.
			writeJSON(w, http.StatusOK, SweepPositionResponse{Present: false, Slug: ""})
			return
		}
		slug := strings.TrimSpace(string(raw))
		if slug == "" {
			writeJSON(w, http.StatusOK, SweepPositionResponse{Present: false, Slug: ""})
			return
		}
		writeJSON(w, http.StatusOK, SweepPositionResponse{Present: true, Slug: slug})
	}
}
