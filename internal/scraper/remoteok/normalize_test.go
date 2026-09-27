// normalize_test.go: tests for the real normalizer.
//
// These replace the tests that pinned the stub's "accept nothing" behavior. The
// stub is gone, and with it the contract that every element produced a skip.
// design: docs/remoteok-mapping.md (locked decisions) and docs/scraper-design.md
// (Run lifecycle step 7, Idempotency contract, Freshness contract).
package remoteok

import (
	"encoding/json"
	"os"
	"testing"
)

// validElement is a complete element in the shape of the real capture: every key
// the mapping doc lists as present on all jobs.
const validElement = `{"id":"1137412","position":"Staff Engineer","company":"Acme Robotics","description":"<p>We build robots.</p>","location":"Berlin","tags":["dev","golang"],"url":"https://remoteOK.com/remote-jobs/staff-engineer-acme-1137412","apply_url":"https://remoteOK.com/remote-jobs/staff-engineer-acme-1137412","epoch":1790006411,"salary_min":10000,"salary_max":750000,"slug":"staff-engineer-acme-1137412","company_logo":"","logo":"","date":"2026-09-21T16:00:11+00:00"}`

// noticeElement is the legal notice: no id, no position, no company.
const noticeElement = `{"last_updated":1790087814,"legal":"API Terms of Service: please link back."}`

// element builds a job element from validElement with fields overridden. It is
// used instead of hand-written fragments so each test varies exactly one thing.
func element(t *testing.T, overrides map[string]any) json.RawMessage {
	t.Helper()

	var fields map[string]any
	if err := json.Unmarshal([]byte(validElement), &fields); err != nil {
		t.Fatalf("decode base element: %v", err)
	}
	for key, value := range overrides {
		fields[key] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode element: %v", err)
	}
	return raw
}

// normalizeOne normalizes a single element that is expected to yield exactly one
// job, and returns it.
func normalizeOne(t *testing.T, raw json.RawMessage) NormalizedJob {
	t.Helper()

	jobs, seenIDs, skipped, err := Normalize([]json.RawMessage{raw})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", skipped)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if len(seenIDs) != 1 {
		t.Fatalf("seenIDs = %v, want exactly one entry", seenIDs)
	}
	return jobs[0]
}

func TestNormalizeKeepsNoticeAndMissingIDOutOfBothSets(t *testing.T) {
	// A job-shaped element with no id: has position and company, so it is not the
	// notice.
	noID := json.RawMessage(`{"position":"No Id Here","company":"Acme Robotics","description":"x","location":"","tags":[],"url":"https://remoteOK.com/remote-jobs/x-1","apply_url":"https://remoteOK.com/remote-jobs/x-1","epoch":1790006411}`)

	jobs, seenIDs, skipped, err := Normalize([]json.RawMessage{
		json.RawMessage(noticeElement),
		json.RawMessage(validElement),
		noID,
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].ExternalID != "1137412" {
		t.Errorf("jobs[0].ExternalID = %q, want 1137412", jobs[0].ExternalID)
	}

	if len(seenIDs) != 1 {
		t.Fatalf("seenIDs = %v, want exactly one entry", seenIDs)
	}
	if _, ok := seenIDs["1137412"]; !ok {
		t.Errorf("seenIDs = %v, want it to contain 1137412", seenIDs)
	}

	if len(skipped) != 1 {
		t.Fatalf("skipped = %+v, want exactly one entry", skipped)
	}
	if skipped[0].Index != 2 {
		t.Errorf("skipped[0].Index = %d, want 2 (the id-less job)", skipped[0].Index)
	}
	if skipped[0].Reason != "missing_id" {
		t.Errorf("skipped[0].Reason = %q, want missing_id", skipped[0].Reason)
	}
}

func TestNormalizeZeroSalaryBecomesNull(t *testing.T) {
	job := normalizeOne(t, element(t, map[string]any{"salary_min": 0, "salary_max": 0}))

	if job.SalaryMinCents != nil {
		t.Errorf("SalaryMinCents = %d, want nil for a 0 salary", *job.SalaryMinCents)
	}
	if job.SalaryMaxCents != nil {
		t.Errorf("SalaryMaxCents = %d, want nil for a 0 salary", *job.SalaryMaxCents)
	}
}

func TestNormalizeDollarSalaryBecomesCents(t *testing.T) {
	job := normalizeOne(t, element(t, map[string]any{"salary_min": 10000, "salary_max": 750000}))

	if job.SalaryMinCents == nil || *job.SalaryMinCents != 1000000 {
		t.Errorf("SalaryMinCents = %v, want 1000000 (10000 dollars)", job.SalaryMinCents)
	}
	if job.SalaryMaxCents == nil || *job.SalaryMaxCents != 75000000 {
		t.Errorf("SalaryMaxCents = %v, want 75000000 (750000 dollars)", job.SalaryMaxCents)
	}
}

func TestNormalizeEmptyLocationIsNil(t *testing.T) {
	empty := normalizeOne(t, element(t, map[string]any{"location": ""}))
	if empty.LocationText != nil {
		t.Errorf("LocationText = %q, want nil for an empty location", *empty.LocationText)
	}

	present := normalizeOne(t, element(t, map[string]any{"location": "Berlin"}))
	if present.LocationText == nil || *present.LocationText != "Berlin" {
		t.Errorf("LocationText = %v, want Berlin", present.LocationText)
	}
}

func TestNormalizeEmptyTagsArrayIsNotNil(t *testing.T) {
	job := normalizeOne(t, element(t, map[string]any{"tags": []any{}}))

	if job.TagsJSON == nil {
		t.Fatal("TagsJSON is nil, want \"[]\" - an empty array must be recorded as present, not missing")
	}
	if *job.TagsJSON != "[]" {
		t.Errorf("TagsJSON = %q, want []", *job.TagsJSON)
	}
}

func TestNormalizeRawDataIsTheOriginalElement(t *testing.T) {
	raw := element(t, nil)
	job := normalizeOne(t, raw)

	if job.RawData != string(raw) {
		t.Errorf("RawData = %q, want the original element verbatim %q", job.RawData, string(raw))
	}
	if !json.Valid([]byte(job.RawData)) {
		t.Error("RawData is not valid JSON")
	}
}

// This is invariant A from docs/scraper-design.md: an element that yields an id
// but then fails normalization must keep its place in the seen set, so a mapping
// regression can never mass-close live jobs.
func TestNormalizeKeepsSeenIDWhenTheRowFails(t *testing.T) {
	// tags as a string is not a shape the mapping accepts (the payload always
	// carries an array), so the element cannot become a row.
	bad := element(t, map[string]any{"tags": "dev"})

	jobs, seenIDs, skipped, err := Normalize([]json.RawMessage{bad})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if len(jobs) != 0 {
		t.Fatalf("got %d jobs, want 0", len(jobs))
	}
	if _, ok := seenIDs["1137412"]; !ok {
		t.Fatalf("seenIDs = %v, want it to keep 1137412 even though the row failed", seenIDs)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %+v, want exactly one entry", skipped)
	}
	if skipped[0].Index != 0 || skipped[0].Reason == "" {
		t.Errorf("skipped[0] = %+v, want an indexed entry with a reason", skipped[0])
	}
}

func TestNormalizeNilElementsIsACatastrophicFailure(t *testing.T) {
	// A nil slice is a caller-side bug: Parse never produces one. It is the only
	// error Normalize returns; every per-element problem is a skip.
	if _, _, _, err := Normalize(nil); err == nil {
		t.Fatal("Normalize(nil) returned no error, want one")
	}
}

// TestNormalizeAgainstRealCapture runs the normalizer over the stored payload
// and asserts the decisions that hold for every element of it. It skips when the
// capture is absent so a fresh clone still passes.
func TestNormalizeAgainstRealCapture(t *testing.T) {
	const capture = "../../../data/raw/remoteok-2026-09-22.json"
	body, err := os.ReadFile(capture)
	if err != nil {
		t.Skipf("no raw capture at %s: %v", capture, err)
	}

	elements, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse on the real capture: %v", err)
	}

	jobs, seenIDs, skipped, err := Normalize(elements)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	// Exactly one element is not a job - the legal notice - and it is neither a
	// row nor a skip.
	if len(jobs)+len(skipped) != len(elements)-1 {
		t.Fatalf("jobs+skips = %d for %d elements, want one non-job element", len(jobs)+len(skipped), len(elements))
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %+v, want none on the real capture", skipped)
	}
	if len(seenIDs) != len(jobs) {
		t.Errorf("seenIDs = %d, jobs = %d, want equal (the capture has no duplicate ids)", len(seenIDs), len(jobs))
	}
	if len(jobs) == 0 {
		t.Fatal("no jobs normalized from the real capture")
	}

	for _, job := range jobs {
		if job.ExternalID == "" || job.Title == "" || job.ListingURL == "" {
			t.Errorf("job %q has an empty required field: title=%q url=%q", job.ExternalID, job.Title, job.ListingURL)
		}
		if job.CompanyName == "" {
			t.Errorf("job %q has an empty company name", job.ExternalID)
		}
		if job.ApplicationURL == nil || *job.ApplicationURL != job.ListingURL {
			t.Errorf("job %q application_url = %v, want it to equal listing_url %q (decision 6)",
				job.ExternalID, job.ApplicationURL, job.ListingURL)
		}
		if job.Description == nil || *job.Description == "" {
			t.Errorf("job %q has no description", job.ExternalID)
		}
		if job.PostedAt == nil || job.PostedAt.IsZero() {
			t.Errorf("job %q has no posted_at", job.ExternalID)
		}
		if job.TagsJSON == nil || !json.Valid([]byte(*job.TagsJSON)) {
			t.Errorf("job %q tags_json = %v, want valid JSON", job.ExternalID, job.TagsJSON)
		}
		if !job.IsRemote {
			t.Errorf("job %q is_remote = false, want true for a remote-only board", job.ExternalID)
		}
		// Decisions that are constant NULL for this source.
		if job.DiscoveryURL != nil {
			t.Errorf("job %q discovery_url = %q, want nil (decision 7)", job.ExternalID, *job.DiscoveryURL)
		}
		if job.EmploymentType != nil {
			t.Errorf("job %q employment_type = %q, want nil (decision 8)", job.ExternalID, *job.EmploymentType)
		}
		if job.Country != nil || job.IsUS != nil {
			t.Errorf("job %q country/is_us = %v/%v, want nil (decision 8)", job.ExternalID, job.Country, job.IsUS)
		}
		if job.SalaryCurrency != nil || job.SalaryPeriod != nil {
			t.Errorf("job %q salary_currency/period = %v/%v, want nil (decisions 3 and 4)",
				job.ExternalID, job.SalaryCurrency, job.SalaryPeriod)
		}
	}
}
