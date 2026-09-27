// normalize.go: the normalization step (design: Run lifecycle step 7).
//
// Every decision here comes from docs/remoteok-mapping.md, whose "Mapping
// decisions (locked)" table resolves the questions the document originally left
// open. Where the mapping says a column is constant NULL for this source, the
// constant is written literally below with the decision number, so a reader can
// trace it back.
package remoteok

import (
	"encoding/json"
	"fmt"
	"time"
)

// NormalizedJob is one job element reduced to the job_listings columns this
// source populates. Nullable columns are pointers so the store can hand them to
// database/sql unchanged and SQLite receives NULL rather than a zero value.
//
// This type is the input to store.UpsertJob, so the store package imports this
// one; the dependency runs store -> remoteok and never the other way.
type NormalizedJob struct {
	ExternalID     string
	CompanyName    string
	Title          string
	ListingURL     string
	ApplicationURL *string
	DiscoveryURL   *string
	EmploymentType *string
	IsRemote       bool
	LocationText   *string
	Country        *string
	IsUS           *bool
	Description    *string
	SalaryMinCents *int64
	SalaryMaxCents *int64
	SalaryCurrency *string
	SalaryPeriod   *string
	TagsJSON       *string
	PostedAt       *time.Time
	RawData        string
}

// NormalizeSkip records one element that produced no normalized row.
type NormalizeSkip struct {
	// Index is the element's position in the parsed response array.
	Index int
	// Reason is a short, log-safe explanation. It must never contain job text
	// (design: Observability / Not logged).
	Reason string
}

// Skip reasons. They reach stderr at debug level, so they name the defect and
// nothing else.
const (
	reasonUnmarshalFailed = "unmarshal_failed"
	reasonMissingID       = "missing_id"
)

// Normalize converts parsed response elements into rows ready for upsert, and
// returns the run's seen set.
//
// Two invariants from docs/scraper-design.md (Run lifecycle step 7 and the
// Freshness contract) drive the shape of this loop:
//
//	A. The identifier is extracted into seenIDs before the element is decoded
//	   any further. An element that yields an id and then fails normalization
//	   keeps its entry, so a mapping regression can only ever mean "this row was
//	   not updated" - never "this row was closed".
//	B. An element with no usable identifier is absent from BOTH jobs and
//	   seenIDs: the two sets are built from the same predicate and cannot
//	   disagree about identity (Idempotency contract, Precondition).
//
// The one exception is the legal notice, which is not a job at all. It is
// detected as the element that has no id, no position and no company
// (docs/remoteok-mapping.md, "Source facts") and produces neither a job nor a
// skip - there is nothing to report.
//
// Per-element problems never fail the run. The only error returned is for a
// caller-side bug: a nil elements slice, which Parse never produces.
func Normalize(elements []json.RawMessage) (jobs []NormalizedJob, seenIDs map[string]struct{}, skipped []NormalizeSkip, err error) {
	if elements == nil {
		return nil, nil, nil, fmt.Errorf("remoteok: normalize called with a nil elements slice")
	}

	jobs = []NormalizedJob{}
	seenIDs = map[string]struct{}{}
	skipped = []NormalizeSkip{}

	for i, raw := range elements {
		// Decode generically first. A typed decode into Job fails as a whole when
		// one field has an unexpected type, which would lose the identifier; this
		// pass cannot, so invariant A holds even for a malformed element.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			skipped = append(skipped, NormalizeSkip{Index: i, Reason: reasonUnmarshalFailed})
			continue
		}

		id, idErr := Job{IDRaw: fields["id"]}.ID()
		if idErr != nil {
			_, hasPosition := fields["position"]
			_, hasCompany := fields["company"]
			if !hasPosition && !hasCompany {
				// The legal notice: not a job, and not a failure.
				continue
			}
			// An identifier-less job must not be inserted with a NULL external_id,
			// which would fall outside the partial unique index and disable dedupe.
			skipped = append(skipped, NormalizeSkip{Index: i, Reason: reasonMissingID})
			continue
		}
		seenIDs[id] = struct{}{}

		var job Job
		if err := json.Unmarshal(raw, &job); err != nil {
			// The id is already in the seen set (invariant A); the element simply
			// does not become a row this run.
			skipped = append(skipped, NormalizeSkip{Index: i, Reason: reasonUnmarshalFailed})
			continue
		}

		normalized, reason := buildJob(job, raw)
		if reason != "" {
			skipped = append(skipped, NormalizeSkip{Index: i, Reason: reason})
			continue
		}
		jobs = append(jobs, normalized)
	}

	return jobs, seenIDs, skipped, nil
}

// buildJob maps one decoded element onto its row. A non-empty reason means the
// element must be skipped; the caller keeps its id in the seen set either way.
func buildJob(job Job, raw json.RawMessage) (NormalizedJob, string) {
	id, err := job.ID()
	if err != nil {
		// Unreachable: the caller only gets here after ID() succeeded. Reported
		// rather than ignored so a future reordering cannot insert a NULL id.
		return NormalizedJob{}, reasonMissingID
	}

	// decisions 1 and 2: 0 means "not specified"; anything else is whole dollars.
	salaryMin, reason := salaryCents(job.SalaryMin, "salary_min")
	if reason != "" {
		return NormalizedJob{}, reason
	}
	salaryMax, reason := salaryCents(job.SalaryMax, "salary_max")
	if reason != "" {
		return NormalizedJob{}, reason
	}

	// tags: serialized as-is, and a missing key is recorded as an empty array
	// rather than as a missing value, so tags_json is always valid JSON.
	tags, err := json.Marshal(nonNilTags(job.Tags))
	if err != nil {
		return NormalizedJob{}, "tags_not_serializable"
	}
	tagsJSON := string(tags)

	// decision 5: epoch seconds, formatted later as RFC3339 UTC by the store.
	postedAt := time.Unix(job.Epoch, 0).UTC()

	// An empty location is "not stated", never a present-but-empty value.
	var location *string
	if job.Location != "" {
		location = &job.Location
	}

	applicationURL := job.ApplyURL
	description := job.Description

	return NormalizedJob{
		ExternalID:     id,
		CompanyName:    job.Company,
		Title:          job.Title,
		ListingURL:     job.URL,
		ApplicationURL: &applicationURL, // decision 6: apply_url
		DiscoveryURL:   nil,             // decision 7
		EmploymentType: nil,             // decision 8
		IsRemote:       true,            // RemoteOK is a remote-only board
		LocationText:   location,
		Country:        nil,          // decision 8
		IsUS:           nil,          // decision 8
		Description:    &description, // stored verbatim; no mojibake repair
		SalaryMinCents: salaryMin,
		SalaryMaxCents: salaryMax,
		SalaryCurrency: nil, // decision 4
		SalaryPeriod:   nil, // decision 3
		TagsJSON:       &tagsJSON,
		PostedAt:       &postedAt,
		RawData:        string(raw),
	}, ""
}

// salaryCents converts a whole-dollar salary into cents. It returns a reason
// string instead of an error because a bad salary is a per-element mapping
// failure, not a run failure (design: Failure policy - row-level problems never
// abort the run).
func salaryCents(raw json.RawMessage, field string) (*int64, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, ""
	}

	var dollars int64
	if err := json.Unmarshal(raw, &dollars); err != nil {
		return nil, field + "_not_integer"
	}
	if dollars == 0 {
		return nil, ""
	}

	cents := dollars * 100
	return &cents, ""
}

// nonNilTags keeps an absent tags key from serializing as JSON null: the column
// records "present but empty" rather than "missing".
func nonNilTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}
