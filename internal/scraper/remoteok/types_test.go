// types_test.go: unit tests for the Job wire type and its identifier accessor.
package remoteok

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodeJob is a test helper: build a Job from a JSON fragment.
func decodeJob(t *testing.T, raw string) Job {
	t.Helper()
	var job Job
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return job
}

func TestJobIDAcceptsString(t *testing.T) {
	id, err := decodeJob(t, `{"id":"1137412"}`).ID()
	if err != nil {
		t.Fatalf("ID() returned error: %v", err)
	}
	if id != "1137412" {
		t.Errorf("ID() = %q, want 1137412", id)
	}
}

func TestJobIDAcceptsNumber(t *testing.T) {
	// The wire type is unconfirmed, so a bare number must work too.
	id, err := decodeJob(t, `{"id":1137412}`).ID()
	if err != nil {
		t.Fatalf("ID() returned error for a numeric id: %v", err)
	}
	if id != "1137412" {
		t.Errorf("ID() = %q, want 1137412", id)
	}
}

func TestJobIDPreservesLiteralDigits(t *testing.T) {
	// json.Number keeps the literal text, so a zero-padded id is not silently
	// rewritten into a different string.
	id, err := decodeJob(t, `{"id":"0001234"}`).ID()
	if err != nil {
		t.Fatalf("ID() returned error: %v", err)
	}
	if id != "0001234" {
		t.Errorf("ID() = %q, want the literal 0001234", id)
	}
}

func TestJobIDRejectsUnusableValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"absent", `{"position":"Engineer"}`},
		{"explicit null", `{"id":null}`},
		{"empty string", `{"id":""}`},
		{"object", `{"id":{"nested":1}}`},
		{"array", `{"id":["1137412"]}`},
		{"boolean", `{"id":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if id, err := decodeJob(t, tc.raw).ID(); err == nil {
				t.Errorf("ID() = %q with no error, want an error so the element is skipped", id)
			}
		})
	}
}

// design: Idempotency contract, Precondition - an element with no usable
// identifier must be reported rather than guessed at.
func TestJobIDNeverInventsAFallback(t *testing.T) {
	// No slug, url, or position may be used as a substitute identifier.
	job := decodeJob(t, `{"slug":"remote-people-ops-1137412","url":"https://remoteOK.com/x","position":"Engineer"}`)
	if id, err := job.ID(); err == nil {
		t.Fatalf("ID() = %q with no error, want an error when id is absent even though slug exists", id)
	}
}

func TestJobIDErrorMentionsID(t *testing.T) {
	_, err := decodeJob(t, `{}`).ID()
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("error = %q, want it to mention the id field", err)
	}
}

func TestJobDecodesAllObservedKeys(t *testing.T) {
	// Every key observed in the real capture must land somewhere on Job, even if
	// the field is only kept for a later mapping decision.
	raw := `{
		"slug":"remote-x-1","id":"1","epoch":1790006411,"date":"2026-09-21T16:00:11+00:00",
		"company":"Ashby","company_logo":"","position":"Engineer","tags":["dev","golang"],
		"description":"<p>hi</p>","location":"Remote","logo":"","url":"https://remoteOK.com/a",
		"apply_url":"https://remoteOK.com/a","salary_min":10000,"salary_max":20000,
		"original":true,"verified":true
	}`
	job := decodeJob(t, raw)

	if job.Slug != "remote-x-1" {
		t.Errorf("Slug = %q", job.Slug)
	}
	if job.Company != "Ashby" {
		t.Errorf("Company = %q", job.Company)
	}
	if job.Title != "Engineer" {
		t.Errorf("Title = %q, want the position value", job.Title)
	}
	if job.Location != "Remote" {
		t.Errorf("Location = %q", job.Location)
	}
	if job.URL != "https://remoteOK.com/a" || job.ApplyURL != "https://remoteOK.com/a" {
		t.Errorf("URL/ApplyURL = %q/%q", job.URL, job.ApplyURL)
	}
	if job.Epoch != 1790006411 {
		t.Errorf("Epoch = %d", job.Epoch)
	}
	if job.Date != "2026-09-21T16:00:11+00:00" {
		t.Errorf("Date = %q", job.Date)
	}
	if len(job.Tags) != 2 || job.Tags[0] != "dev" {
		t.Errorf("Tags = %v", job.Tags)
	}
	if string(job.SalaryMin) != "10000" || string(job.SalaryMax) != "20000" {
		t.Errorf("SalaryMin/Max = %s/%s, want the raw JSON numbers", job.SalaryMin, job.SalaryMax)
	}
	if job.Description == "" || job.CompanyLogo != "" {
		t.Errorf("Description/CompanyLogo = %q/%q", job.Description, job.CompanyLogo)
	}
}

func TestJobSalaryFieldsSurviveNullAndZero(t *testing.T) {
	// Salary is unmapped while the normalizer is stubbed, but the raw JSON must
	// be preserved so a later mapping can read it without re-fetching.
	job := decodeJob(t, `{"id":"1","salary_min":0,"salary_max":null}`)
	if string(job.SalaryMin) != "0" {
		t.Errorf("SalaryMin = %s, want the literal 0", job.SalaryMin)
	}
	if string(job.SalaryMax) != "null" {
		t.Errorf("SalaryMax = %s, want the literal null", job.SalaryMax)
	}
}

func TestJobMissingTagsDecodesToNil(t *testing.T) {
	job := decodeJob(t, `{"id":"1"}`)
	if job.Tags != nil {
		t.Errorf("Tags = %v, want nil when the key is absent", job.Tags)
	}
}
