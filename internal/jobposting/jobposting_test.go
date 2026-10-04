package jobposting

import (
	"testing"
	"time"
)

// eightfoldPosting is trimmed from a real raw_data blob captured from an Eightfold board: the field
// spellings, the timezone-less datePosted, and the string employmentType are all as the board wrote
// them. baseSalary is absent, which is what the live rows showed too.
const eightfoldPosting = `{
  "@context": "http://schema.org/",
  "@type": "JobPosting",
  "title": "Senior Software Engineer",
  "datePosted": "2026-02-06T21:01:54",
  "employmentType": "FULL_TIME",
  "jobLocationType": "TELECOMMUTE",
  "validThrough": "2026-06-06T21:01:54",
  "description": "<p>Build things.</p>",
  "hiringOrganization": {"@type": "Organization", "name": "Twilio"},
  "applicantLocationRequirements": [{"@type": "Country", "name": "US"}]
}`

func TestParseReadsTheStructuredFields(t *testing.T) {
	got, found := Parse(eightfoldPosting)
	if !found {
		t.Fatal("found = false, want a JobPosting")
	}
	if got.Remote == nil || !*got.Remote {
		t.Errorf("Remote = %v, want true (TELECOMMUTE)", got.Remote)
	}
	if got.EmploymentType != "full_time" {
		t.Errorf("EmploymentType = %q, want full_time (schema.org FULL_TIME folded onto the column's vocabulary)", got.EmploymentType)
	}
	want := time.Date(2026, 2, 6, 21, 1, 54, 0, time.UTC)
	if got.PostedAt == nil || !got.PostedAt.Equal(want) {
		t.Errorf("PostedAt = %v, want %v (a timezone-less datePosted is read as UTC)", got.PostedAt, want)
	}
	wantThrough := time.Date(2026, 6, 6, 21, 1, 54, 0, time.UTC)
	if got.ValidThrough == nil || !got.ValidThrough.Equal(wantThrough) {
		t.Errorf("ValidThrough = %v, want %v", got.ValidThrough, wantThrough)
	}
}

// The rule that matters most: a board that stays silent about remote must not be read as saying
// on-site, or every posting from such a board is silently dropped.
func TestParseMissingLocationTypeIsUnknownNotOnSite(t *testing.T) {
	got, found := Parse(`{"@type":"JobPosting","datePosted":"2026-02-06","description":"d"}`)
	if !found {
		t.Fatal("found = false, want a JobPosting")
	}
	if got.Remote != nil {
		t.Errorf("Remote = %v, want nil for an absent jobLocationType", *got.Remote)
	}
}

func TestParseHandlesArrayValuedFields(t *testing.T) {
	got, found := Parse(`{"@type":["JobPosting"],"employmentType":["CONTRACTOR","FULL_TIME"],"description":"d"}`)
	if !found {
		t.Fatal("found = false, want a JobPosting (an @type array is still a JobPosting)")
	}
	if got.EmploymentType != "contract" {
		t.Errorf("EmploymentType = %q, want contract (first array entry)", got.EmploymentType)
	}
}

func TestParseLeavesUnmappableEmploymentTypeEmpty(t *testing.T) {
	got, _ := Parse(`{"@type":"JobPosting","employmentType":"VOLUNTEER","description":"d"}`)
	if got.EmploymentType != "" {
		t.Errorf("EmploymentType = %q, want \"\" so the column stays NULL rather than claiming 'unknown'", got.EmploymentType)
	}
}

func TestParseDropsUnparseableDates(t *testing.T) {
	got, _ := Parse(`{"@type":"JobPosting","datePosted":"yesterday-ish","description":"d"}`)
	if got.PostedAt != nil {
		t.Errorf("PostedAt = %v, want nil: a wrong posted date is worse than an absent one", *got.PostedAt)
	}
}

func TestParseAcceptsAnExplicitTimezone(t *testing.T) {
	got, _ := Parse(`{"@type":"JobPosting","datePosted":"2026-02-06T21:01:54Z","description":"d"}`)
	want := time.Date(2026, 2, 6, 21, 1, 54, 0, time.UTC)
	if got.PostedAt == nil || !got.PostedAt.Equal(want) {
		t.Errorf("PostedAt = %v, want %v", got.PostedAt, want)
	}
}

// The RemoteOK feed's raw_data is also JSON and also has a description; it must not be mistaken for a
// JobPosting, or the backfill would try to read postings out of it.
func TestParseRejectsNonJobPostingJSON(t *testing.T) {
	cases := map[string]string{
		"remoteok element": `{"id":"1137412","position":"Staff Engineer","company":"Acme","description":"<p>hi</p>"}`,
		"empty":            "",
		"not json":         "nope",
		"array":            `[{"@type":"JobPosting"}]`,
	}
	for name, raw := range cases {
		if _, found := Parse(raw); found {
			t.Errorf("%s: found = true, want false", name)
		}
	}
}
