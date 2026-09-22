// handlers.go: the two request handlers and the JSON response helpers.
//
// Handlers do three things and no more: parse/validate input, map store rows to
// the wire shapes in types.go, and write a response. They perform no human
// formatting - no currency symbols, no relative dates, no display labels - so
// the SvelteKit UI owns presentation and the API owns the data contract.
package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"jobsapp/internal/store"
)

const (
	// Pagination bounds from the API contract in docs/ui-design.md.
	defaultLimit  = 25
	minLimit      = 1
	maxLimit      = 100
	defaultOffset = 0

	// unknownCompany is the fallback when the companies join does not resolve.
	// company_id is NOT NULL with foreign_keys ON, so this is defensive only.
	unknownCompany = "Unknown company"

	// Machine codes for the error envelope.
	codeBadRequest       = "bad_request"
	codeNotFound         = "not_found"
	codeInternal         = "internal"
	codeMethodNotAllowed = "method_not_allowed"
	contentTypeJSON      = "application/json; charset=utf-8"
	internalErrorMessage = "internal server error"
)

// handleListJobs serves GET /api/jobs.
func handleListJobs(db *sql.DB) http.HandlerFunc {
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

		rows, total, err := store.ListJobs(r.Context(), db, limit, offset)
		if err != nil {
			writeInternalError(w, err)
			return
		}

		// Non-nil so an empty page marshals as "jobs":[] and not "jobs":null.
		items := make([]JobListItem, 0, len(rows))
		for _, row := range rows {
			item, err := toListItem(row)
			if err != nil {
				writeInternalError(w, err)
				return
			}
			items = append(items, item)
		}

		writeJSON(w, http.StatusOK, ListResponse{
			Jobs:   items,
			Limit:  limit,
			Offset: offset,
			Total:  total,
		})
	}
}

// handleGetJob serves GET /api/jobs/{id}.
func handleGetJob(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only a non-numeric id is a bad request. A numeric id that matches no
		// row (including 0 or a negative value) is a 404, not a 400: the
		// identifier is well formed, the resource is simply not there.
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "job id must be an integer")
			return
		}

		row, found, err := store.GetJobByID(r.Context(), db, id)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("no job with id %d", id))
			return
		}

		item, err := toListItem(row)
		if err != nil {
			writeInternalError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, JobDetail{
			JobListItem:    item,
			Description:    nullableString(row.Description),
			ListingURL:     row.ListingURL,
			ApplicationURL: nullableString(row.ApplicationURL),
			DiscoveryURL:   nullableString(row.DiscoveryURL),
		})
	}
}

// intQueryParam returns the named query parameter as an int, or def when the
// parameter is absent or empty (an empty value is treated as unset, matching
// the config package's convention). Bounds are checked by the caller so each
// rejection can name the parameter and its range.
func intQueryParam(r *http.Request, name string, def int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return v, nil
}

// toListItem maps one store row onto the list-item contract. Every error it
// returns is a "should never happen" schema violation (a non-0/1 is_remote, a
// non-RFC3339 timestamp); the caller turns those into a 500 rather than
// guessing a value.
func toListItem(row store.JobRow) (JobListItem, error) {
	isRemote, err := remoteBool(row.ID, row.IsRemote)
	if err != nil {
		return JobListItem{}, err
	}
	postedAt, err := nullableTimestamp(row.ID, "posted_at", row.PostedAt)
	if err != nil {
		return JobListItem{}, err
	}
	firstSeenAt, err := timestamp(row.ID, "first_seen_at", row.FirstSeenAt)
	if err != nil {
		return JobListItem{}, err
	}
	lastSeenAt, err := timestamp(row.ID, "last_seen_at", row.LastSeenAt)
	if err != nil {
		return JobListItem{}, err
	}

	companyName := row.CompanyName.String
	if !row.CompanyName.Valid {
		companyName = unknownCompany
	}

	return JobListItem{
		ID:             row.ID,
		Title:          row.Title,
		CompanyName:    companyName,
		Status:         row.Status,
		EmploymentType: nullableString(row.EmploymentType),
		LocationText:   nullableString(row.LocationText),
		Country:        nullableString(row.Country),
		IsRemote:       isRemote,
		Salary: Salary{
			MinCents: nullableInt64(row.SalaryMinCents),
			MaxCents: nullableInt64(row.SalaryMaxCents),
			Currency: nullableString(row.SalaryCurrency),
			Period:   nullableString(row.SalaryPeriod),
		},
		PostedAt:    postedAt,
		FirstSeenAt: firstSeenAt,
		LastSeenAt:  lastSeenAt,
	}, nil
}

// remoteBool converts the is_remote INTEGER (0/1, enforced by a schema CHECK)
// into the boolean the contract specifies.
func remoteBool(jobID, value int64) (bool, error) {
	switch value {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("job %d: is_remote = %d, want 0 or 1", jobID, value)
	}
}

// timestamp parses a NOT NULL SQLite TEXT timestamp and re-emits it as RFC3339
// UTC, so the wire format is identical whatever precision the writer used.
func timestamp(jobID int64, column, raw string) (string, error) {
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", fmt.Errorf("job %d: %s %q is not RFC3339: %w", jobID, column, raw, err)
	}
	return ts.UTC().Format(time.RFC3339), nil
}

// nullableTimestamp is timestamp for a nullable column: an invalid NullString
// becomes a JSON null, not an empty string.
func nullableTimestamp(jobID int64, column string, v sql.NullString) (*string, error) {
	if !v.Valid {
		return nil, nil
	}
	s, err := timestamp(jobID, column, v.String)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func nullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullableInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// writeJSON marshals body and writes it with the documented Content-Type. A
// marshal failure cannot be reported in the response's own shape, so it falls
// back to a hand-written internal error envelope.
func writeJSON(w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		log.Printf("api: marshal response: %v", err)
		payload = []byte(`{"error":{"code":"internal","message":"internal server error"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// writeError writes the single error envelope shape every non-2xx uses.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Error: ErrorBody{Code: code, Message: message}})
}

// writeInternalError logs the real error for the operator and tells the client
// only that something broke. SQL text, column names, and file paths stay in the
// log, never in the response body.
func writeInternalError(w http.ResponseWriter, err error) {
	log.Printf("api: internal error: %v", err)
	writeError(w, http.StatusInternalServerError, codeInternal, internalErrorMessage)
}
