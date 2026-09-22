// types.go: the wire shapes of the read-only JSON API.
//
// These structs are the contract in docs/ui-design.md (API contract). Field
// names, nullability, and the deliberate omissions are all load-bearing:
// nullable columns are pointers so they marshal to JSON null (never "" or 0),
// and nothing here carries omitempty because the UI must be able to tell
// "absent from the contract" apart from "present and null".
package api

// JobListItem is one entry of the GET /api/jobs response.
type JobListItem struct {
	ID             int64   `json:"id"`
	Title          string  `json:"title"`
	CompanyName    string  `json:"company_name"`
	Status         string  `json:"status"`
	EmploymentType *string `json:"employment_type"`
	LocationText   *string `json:"location_text"`
	Country        *string `json:"country"`
	IsRemote       bool    `json:"is_remote"`
	Salary         Salary  `json:"salary"`
	PostedAt       *string `json:"posted_at"`
	FirstSeenAt    string  `json:"first_seen_at"`
	LastSeenAt     string  `json:"last_seen_at"`
}

// Salary groups the four compensation columns. All four are nullable in
// job_listings, so all four are pointers.
type Salary struct {
	MinCents *int64  `json:"min_cents"`
	MaxCents *int64  `json:"max_cents"`
	Currency *string `json:"currency"`
	Period   *string `json:"period"`
}

// JobDetail is the GET /api/jobs/{id} response: the list item plus the four
// detail-only fields.
//
// It embeds JobListItem rather than repeating its twelve fields so the two
// endpoints cannot drift apart when the list shape changes. encoding/json
// inlines an embedded struct's fields, so the wire shape stays flat - the
// detail object is not nested under a key.
type JobDetail struct {
	JobListItem
	Description    *string `json:"description"`
	ListingURL     string  `json:"listing_url"`
	ApplicationURL *string `json:"application_url"`
	DiscoveryURL   *string `json:"discovery_url"`
}

// ListResponse is the envelope around GET /api/jobs. Total is the number of
// matching rows, not the number in this page.
type ListResponse struct {
	Jobs   []JobListItem `json:"jobs"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
	Total  int64         `json:"total"`
}

// ErrorResponse is the body of every non-2xx response.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries a short machine code (bad_request, not_found, internal,
// method_not_allowed) and a human-readable message.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
