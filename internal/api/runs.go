// runs.go: the handler behind GET /api/runs.
//
// Same division of labour as the job handlers: parse and validate input, map
// store rows to the wire shape in types.go, write a response. No human
// formatting - the UI owns status labels, relative times and durations, and the
// API owns the data contract.
package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

	"jobsapp/internal/store"
)

// handleListRuns serves GET /api/runs: one page of runs, newest first.
func handleListRuns(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, err := intQueryParam(r, "limit", defaultLimit)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		if limit < minLimit || limit > maxLimit {
			writeError(w, http.StatusBadRequest, codeBadRequest,
				fmt.Sprintf("limit must be between %d and %d", minLimit, maxLimit))
			return
		}

		offset, err := intQueryParam(r, "offset", defaultOffset)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		if offset < 0 {
			writeError(w, http.StatusBadRequest, codeBadRequest, "offset must not be negative")
			return
		}

		rows, total, err := store.ListRuns(r.Context(), db, limit, offset)
		if err != nil {
			writeInternalError(w, fmt.Errorf("list runs: %w", err))
			return
		}

		// Non-nil so an empty page marshals as "runs":[] and not "runs":null.
		items := make([]RunListItem, 0, len(rows))
		for _, row := range rows {
			item, err := runToWire(row)
			if err != nil {
				writeInternalError(w, err)
				return
			}
			items = append(items, item)
		}

		writeJSON(w, http.StatusOK, RunListResponse{
			Runs:   items,
			Limit:  limit,
			Offset: offset,
			Total:  total,
		})
	}
}

// handleGetRun serves GET /api/runs/{id}: one run, so the run detail page can show its status and
// link to its trace.
func handleGetRun(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "run id must be an integer")
			return
		}

		row, err := store.GetRun(r.Context(), db, id)
		if err != nil {
			if err == sql.ErrNoRows {
				writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("no run with id %d", id))
				return
			}
			writeInternalError(w, fmt.Errorf("get run %d: %w", id, err))
			return
		}

		item, err := runToWire(row)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

// runToWire maps one store row onto the wire contract. The only failure it can
// return is a non-RFC3339 timestamp or a non-0/1 dry_run, both schema
// violations the caller turns into a 500 rather than guessing a value.
func runToWire(row store.RunRow) (RunListItem, error) {
	startedAt, err := timestamp(row.ID, "started_at", row.StartedAt)
	if err != nil {
		return RunListItem{}, err
	}
	finishedAt, err := nullableTimestamp(row.ID, "finished_at", row.FinishedAt)
	if err != nil {
		return RunListItem{}, err
	}

	var dryRun bool
	switch row.DryRun {
	case 0:
		dryRun = false
	case 1:
		dryRun = true
	default:
		return RunListItem{}, fmt.Errorf("run %d: dry_run = %d, want 0 or 1", row.ID, row.DryRun)
	}

	return RunListItem{
		ID:                row.ID,
		Platform:          row.Platform,
		Status:            row.Status,
		StartedAt:         startedAt,
		FinishedAt:        finishedAt,
		ItemsFound:        row.ItemsFound,
		ItemsInserted:     row.ItemsInserted,
		ItemsUpdated:      row.ItemsUpdated,
		ItemsWrong:        row.ItemsWrong,
		ItemsUnverifiable: row.ItemsUnverifiable,
		DryRun:            dryRun,
		ErrorText:         nullableString(row.ErrorText),
	}, nil
}
