// companies.go: the two company-directory handlers.
//
// Same division of labour as the job handlers: parse and validate input, map store rows to the wire
// shapes, write a response. No human formatting - the UI owns currency symbols, relative dates and
// display labels, and the API owns the data contract.
package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

	"jobsapp/internal/store"
)

// handleListCompanies serves GET /api/companies.
//
// Filters are validated by the store's filter type rather than here, so an unrecognised value cannot
// reach the SQL as a silent no-op: a misspelled ?resolution= would otherwise return every company,
// which reads as a result rather than as a mistake.
func handleListCompanies(db *sql.DB) http.HandlerFunc {
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

		q := r.URL.Query()
		filter := store.CompanyFilter{
			IndexMembership: q.Get("index"),
			Resolution:      q.Get("resolution"),
			Search:          q.Get("search"),
		}
		if err := filter.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}

		rows, total, err := store.ListCompanies(r.Context(), db, filter, limit, offset)
		if err != nil {
			writeInternalError(w, fmt.Errorf("list companies: %w", err))
			return
		}

		items := make([]CompanyListItem, 0, len(rows))
		for _, row := range rows {
			items = append(items, companyToWire(row))
		}
		writeJSON(w, http.StatusOK, CompanyListResponse{
			Companies: items,
			Limit:     limit,
			Offset:    offset,
			Total:     total,
		})
	}
}

// handleGetCompany serves GET /api/companies/{id}.
func handleGetCompany(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "company id must be an integer")
			return
		}

		row, err := store.GetCompany(r.Context(), db, id)
		if err != nil {
			if err == sql.ErrNoRows {
				writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("no company with id %d", id))
				return
			}
			writeInternalError(w, fmt.Errorf("get company: %w", err))
			return
		}

		attempts, err := store.ListCompanyAttempts(r.Context(), db, id)
		if err != nil {
			writeInternalError(w, fmt.Errorf("list company attempts: %w", err))
			return
		}

		wire := make([]CompanyAttempt, 0, len(attempts))
		for _, a := range attempts {
			wire = append(wire, attemptToWire(a))
		}
		writeJSON(w, http.StatusOK, CompanyDetailResponse{
			Company:  companyToWire(row),
			Attempts: wire,
		})
	}
}

// companyToWire maps a store row to the wire shape, turning every Null* into a pointer so absent
// values marshal to null rather than to a zero value the UI would have to guess about.
func companyToWire(row store.CompanyRow) CompanyListItem {
	return CompanyListItem{
		ID:               row.ID,
		Slug:             row.Slug,
		Name:             row.Name,
		Industry:         nullStringPtr(row.Industry),
		SubIndustry:      nullStringPtr(row.SubIndustry),
		Headquarters:     nullStringPtr(row.Headquarters),
		IndexMembership:  nullStringPtr(row.IndexMembership),
		Website:          nullStringPtr(row.Website),
		WebsiteSource:    nullStringPtr(row.WebsiteSource),
		CareerSiteURL:    nullStringPtr(row.CareerSiteURL),
		CareerSiteSource: nullStringPtr(row.CareerSiteSource),
		CareerSiteTitle:  nullStringPtr(row.CareerSiteTitle),
		AttemptCount:     row.AttemptCount,
		UpdatedAt:        row.UpdatedAt,
	}
}

func attemptToWire(a store.AttemptRow) CompanyAttempt {
	return CompanyAttempt{
		ID:              a.ID,
		RunID:           nullInt64Ptr(a.RunID),
		AttemptIndex:    a.AttemptIndex,
		Source:          a.Source,
		CandidateURL:    a.CandidateURL,
		Kind:            a.Kind,
		HTTPStatus:      nullInt64Ptr(a.HTTPStatus),
		FinalURL:        nullStringPtr(a.FinalURL),
		Title:           nullStringPtr(a.Title),
		ValidationState: a.ValidationState,
		RejectionReason: nullStringPtr(a.RejectionReason),
		EvidencePath:    nullStringPtr(a.EvidencePath),
		CreatedAt:       a.CreatedAt,
	}
}

// nullStringPtr converts a nullable string column to a pointer, so JSON gets null rather than "".
func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
