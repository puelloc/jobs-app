// platforms_test.go: tests for recording a company's application-platform classification.
package store

import (
	"context"
	"testing"
)

func TestSetCompanyApplicationPlatform_InsertsThenRefreshes(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO companies (id, slug, name) VALUES (1, 'twilio', 'Twilio')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	// eightfold = 31, an application_system platform.
	if err := SetCompanyApplicationPlatform(ctx, database, 1, 31, "https://jobs.twilio.com"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Re-setting the same (company, platform) pair refreshes base_url, never duplicates.
	if err := SetCompanyApplicationPlatform(ctx, database, 1, 31, "https://jobs.twilio.com/careers"); err != nil {
		t.Fatalf("re-set: %v", err)
	}

	var count int64
	var baseURL string
	if err := database.QueryRow(
		`SELECT count(*), max(base_url) FROM company_application_platforms WHERE company_id = 1 AND platform_id = 31`).
		Scan(&count, &baseURL); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (no duplicate)", count)
	}
	if baseURL != "https://jobs.twilio.com/careers" {
		t.Errorf("base_url = %q, want the refreshed value", baseURL)
	}
}

func TestSetCompanyApplicationPlatform_RejectsNonApplicationSystem(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO companies (id, slug, name) VALUES (2, 'acme', 'Acme')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	// platform 1 is remoteok, a job board — not an application system.
	if err := SetCompanyApplicationPlatform(ctx, database, 2, 1, ""); err == nil {
		t.Error("expected an error for a non-application_system platform")
	}
}
