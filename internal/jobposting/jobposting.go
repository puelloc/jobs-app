// Package jobposting reads the schema.org JobPosting JSON that a job board embeds in each of its
// postings.
//
// It exists because the listings scrape already fetches that JSON verbatim (worker/listings_fetch.py
// stores it in job_listings.raw_data) and nothing read it: posted_at and employment_type were left
// NULL even when the board had said, and remote was asserted from an assumption about the listings URL
// rather than taken from the data. Everything here is a pure function over the stored blob, so it is
// re-runnable against rows captured before it existed (cmd/backfill).
//
// Nothing here is defaulted. A field the board omitted stays absent, because "the board did not say"
// and "the board said no" are different facts (docs/remoteok-mapping.md: null means not known).
package jobposting

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Posting is the subset of a JobPosting this pipeline stores.
type Posting struct {
	// Remote is true when jobLocationType is TELECOMMUTE, and nil when the board did not say. It is
	// never false: schema.org defines TELECOMMUTE as the only location type, so a missing field is an
	// absence of information rather than a claim that the role is on-site. Treating it as on-site
	// would silently drop every posting from a board that omits the field.
	Remote *bool
	// PostedAt is datePosted. Boards commonly emit it with no timezone, which is read as UTC.
	PostedAt *time.Time
	// EmploymentType is employmentType normalised to the job_listings vocabulary, or "" when the board
	// said nothing or used a value that vocabulary cannot express.
	EmploymentType string
	// ValidThrough is the board's own expiry, when it gives one.
	ValidThrough *time.Time
}

// Parse reads the JobPosting JSON embedded in one posting. found is false when the blob is not a
// JSON object carrying a description, which is what every non-browser source's raw_data looks like.
func Parse(raw string) (Posting, bool) {
	if strings.TrimSpace(raw) == "" {
		return Posting{}, false
	}

	// Decoded into a permissive shape: boards disagree about whether a single-valued field is a string
	// or a one-element array, and a strict struct would reject the whole posting over that.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return Posting{}, false
	}
	if !looksLikeJobPosting(fields) {
		return Posting{}, false
	}

	var p Posting
	if firstString(fields["jobLocationType"]) == "TELECOMMUTE" {
		remote := true
		p.Remote = &remote
	}
	p.EmploymentType = normalizeEmploymentType(firstString(fields["employmentType"]))
	p.PostedAt = parseInstant(firstString(fields["datePosted"]))
	p.ValidThrough = parseInstant(firstString(fields["validThrough"]))
	return p, true
}

// jobPostingMarkers are fields only a schema.org JobPosting carries. The RemoteOK feed's raw_data also
// has a "description", so its presence alone cannot be the test - but it carries none of these.
var jobPostingMarkers = []string{
	"jobLocationType",
	"hiringOrganization",
	"applicantLocationRequirements",
	"validThrough",
	"datePosted",
}

// looksLikeJobPosting reports whether the blob is the schema.org JobPosting a board embeds, rather
// than some other JSON that happens to be in raw_data.
func looksLikeJobPosting(fields map[string]json.RawMessage) bool {
	if strings.EqualFold(firstString(fields["@type"]), "JobPosting") {
		return true
	}
	for _, key := range jobPostingMarkers {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

// firstString reads a field that may be a bare string or an array of them, and returns the first
// non-empty value. Anything else yields "".
func firstString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return strings.TrimSpace(one)
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, s := range many {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// employmentTypes maps schema.org's spelled-out values onto the job_listings vocabulary. schema.org
// uses FULL_TIME / CONTRACTOR; the column's CHECK uses full_time / contract.
var employmentTypes = map[string]string{
	"FULL_TIME":  "full_time",
	"PART_TIME":  "part_time",
	"CONTRACTOR": "contract",
	"CONTRACT":   "contract",
	"INTERN":     "intern",
	"INTERNSHIP": "intern",
	"TEMPORARY":  "temporary",
	"TEMP":       "temporary",
}

// normalizeEmploymentType folds a board's spelling onto the column's vocabulary, returning "" for a
// value the vocabulary cannot express. "" becomes NULL rather than the 'unknown' member: 'unknown' is
// for a source that explicitly says it does not know, and using it here would claim the board spoke.
func normalizeEmploymentType(raw string) string {
	key := strings.ToUpper(strings.TrimSpace(raw))
	key = strings.NewReplacer("-", "_", " ", "_").Replace(key)
	return employmentTypes[key]
}

// instantLayouts are the datePosted / validThrough spellings seen in the wild, most specific first.
// The timezone-less layout is the common one, and is read as UTC: guessing a local zone would invent
// precision the board did not provide, and the field is a posting date, not an instant.
var instantLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseInstant reads a date the way a board writes one, or nil when it is missing or unparseable. An
// unparseable date is dropped rather than stored approximately: a wrong posted_at is worse for the
// reader than an absent one.
func parseInstant(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range instantLayouts {
		ts, err := time.Parse(layout, raw)
		if err == nil {
			ts = ts.UTC()
			return &ts
		}
	}
	return nil
}

// String renders a Posting for logs, so a parsing surprise is visible without a debugger.
func (p Posting) String() string {
	remote := "unknown"
	if p.Remote != nil {
		remote = fmt.Sprintf("%t", *p.Remote)
	}
	posted := ""
	if p.PostedAt != nil {
		posted = p.PostedAt.Format(time.RFC3339)
	}
	return fmt.Sprintf("remote=%s posted_at=%q employment_type=%q", remote, posted, p.EmploymentType)
}
