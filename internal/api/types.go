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

// --- company directory ----------------------------------------------------

// CompanyListItem is one entry of the GET /api/companies response.
//
// Every nullable column is a pointer so it marshals to JSON null rather than "" - the UI must be
// able to tell "no website resolved" from "the website is the empty string". IndexMembership is
// likewise a pointer: a company imported from a source that does not record an index has no
// membership, which is different from belonging to none of them.
type CompanyListItem struct {
	ID               int64   `json:"id"`
	Slug             string  `json:"slug"`
	Name             string  `json:"name"`
	Industry         *string `json:"industry"`
	SubIndustry      *string `json:"gics_sub_industry"`
	Headquarters     *string `json:"headquarters_location"`
	IndexMembership  *string `json:"index_membership"`
	Website          *string `json:"website"`
	WebsiteSource    *string `json:"website_source"`
	CareerSiteURL    *string `json:"career_site_url"`
	CareerSiteSource *string `json:"career_site_source"`
	// CareerSiteTitle is what makes a wrong pick visible in the list without opening the URL.
	CareerSiteTitle *string `json:"career_site_title"`
	// AttemptCount is how many resolution attempts exist across all runs, so an unresolved company
	// can be told apart from one that has never been looked at.
	AttemptCount int64  `json:"attempt_count"`
	UpdatedAt    string `json:"updated_at"`
}

// CompanyListResponse is the GET /api/companies envelope. It mirrors ListResponse rather than
// sharing it, because the two endpoints have different item shapes and a generic wrapper would
// obscure that on the wire.
type CompanyListResponse struct {
	Companies []CompanyListItem `json:"companies"`
	Limit     int               `json:"limit"`
	Offset    int               `json:"offset"`
	Total     int64             `json:"total"`
}

// CompanyAttempt is one candidate the resolver tried for a company.
//
// The full trail is exposed, accepted and rejected alike: a wrong final answer is only diagnosable
// next to the candidates that were refused before it.
type CompanyAttempt struct {
	ID              int64   `json:"id"`
	RunID           *int64  `json:"run_id"`
	AttemptIndex    int64   `json:"attempt_index"`
	Source          string  `json:"source"`
	CandidateURL    string  `json:"candidate_url"`
	Kind            string  `json:"candidate_kind"`
	HTTPStatus      *int64  `json:"http_status"`
	FinalURL        *string `json:"final_url"`
	Title           *string `json:"title"`
	ValidationState string  `json:"validation_status"`
	// RejectionReason is the closed Outcome vocabulary's string form. It is null exactly when the
	// attempt was accepted, which the schema enforces.
	RejectionReason *string `json:"rejection_reason"`
	EvidencePath    *string `json:"evidence_path"`
	CreatedAt       string  `json:"created_at"`
}

// CompanyDetailResponse is the GET /api/companies/{id} response: the list item plus the trail.
type CompanyDetailResponse struct {
	Company  CompanyListItem  `json:"company"`
	Attempts []CompanyAttempt `json:"attempts"`
}
