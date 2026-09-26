// resolutions.go: persistence for one company's careers resolution.
//
// The worker pool fetches and validates; this is the single writer that consumes its results. The
// shape is not incidental: db.Open caps the pool at one connection, so concurrent writers would
// contend on it and could interleave a company's attempts with another's. One writer goroutine makes
// the serialization explicit rather than accidental.
//
// One transaction per company, containing its attempt rows, its companies update, and its
// application-platform upsert. A company that fails rolls back alone and the run continues.
//
// design: docs/sp1500-plan.md, sections 7.1-7.3.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"jobsapp/internal/careers"
)

// ErrEvidenceExists is returned when an evidence path is already occupied.
//
// The path is data/raw/careers/<slug>/<run-id>/<index>-<host>.<ext>, so a collision means the same
// run resolved the same company at the same attempt index twice. That is a bug in the caller, not a
// same-day rerun, which is why it is an error rather than the exit-5 convention used for dated
// captures.
var ErrEvidenceExists = errors.New("store: evidence file already exists for this run slot")

// ResolutionAttempt is one candidate that was tried, mirroring url_resolution_attempts.
type ResolutionAttempt struct {
	Source          string
	CandidateURL    string
	Kind            careers.Kind
	HTTPStatus      int
	FinalURL        string
	Title           string
	ValidationState careers.ValidationStatus
	// Reason is set for every non-accepted attempt and must be empty for an accepted one; the
	// schema enforces that shape.
	Reason careers.Reason
	// EvidencePath points at the stored response body that produced the decision. Empty when the
	// body was not retained, which is allowed: a transport failure has no body.
	EvidencePath string
	// Evidence is a short parsed summary kept in the row itself, such as a JobPosting count. It is
	// the part worth querying without opening the file.
	Evidence string
	// Body is the response that produced the decision, written to EvidencePath by the writer. It is
	// held in memory rather than written by the caller because the writer is the only component that
	// knows the attempt index, and the index is part of the path.
	Body []byte
	// ContentType decides the evidence file's extension.
	ContentType string
}

// Resolution is one company's outcome for a run.
type Resolution struct {
	CompanyID   int64
	CompanySlug string

	// Website is set when tier 1 resolved a homepage. Empty means the company already had one or
	// tier 1 found none.
	Website          string
	WebsiteSource    WebsiteSource
	CareerSiteURL    string
	CareerSiteSource CareerSiteSource
	// CareerSiteStatus and CareerSiteTitle record what the accepted careers fetch returned.
	CareerSiteStatus int
	CareerSiteTitle  string

	// ATSPlatformID is set when the accepted careers URL is a third-party board, and BaseURL is the
	// concrete tenant URL rather than the platform's template.
	ATSPlatformID int64
	ATSBaseURL    string

	// DryRun suppresses every write to the companies table and to company_application_platforms,
	// while still recording the attempt rows. A pre-flight pass has to be non-destructive to be
	// worth running against real data, but its attempts are the whole reason to run it.
	DryRun bool

	Attempts []ResolutionAttempt
}

// Resolved reports whether the company's career site was found.
func (r Resolution) Resolved() bool { return r.CareerSiteURL != "" }

// WriteResult reports what persisting a Resolution did.
type WriteResult struct {
	// AttemptsInserted is how many attempt rows were written.
	AttemptsInserted int
	// CareerSiteInserted is true when career_site_url went from NULL to a value.
	CareerSiteInserted bool
	// CareerSiteUpdated is true when it changed from one value to another.
	CareerSiteUpdated bool
}

// ResolutionWriter persists resolutions. A single instance is used from one goroutine.
type ResolutionWriter struct {
	db *sql.DB
	// dataDir is the root for evidence files. Empty disables evidence retention, which is what the
	// persistence tests that do not care about the files use.
	dataDir string
}

// NewResolutionWriter returns a writer over db, retaining evidence under dataDir.
func NewResolutionWriter(db *sql.DB, dataDir string) *ResolutionWriter {
	return &ResolutionWriter{db: db, dataDir: dataDir}
}

// Write persists one company's resolution in a single transaction.
//
// firstAttemptIndex is the position this resolution's first attempt occupies among all the attempts
// recorded for the company in this run. It is passed in rather than counted from zero here because a
// company can legitimately be written more than once per run - a retried company, or a test writing
// one candidate at a time - and restarting at zero collides with the unique index on
// (run_id, company_id, attempt_index). The index is what the evidence path is built from, so the
// caller keeping it monotonic is what makes the path collision-free by construction.
func (w *ResolutionWriter) Write(ctx context.Context, runID int64, firstAttemptIndex int, res Resolution) (WriteResult, error) {
	var out WriteResult

	if err := validateResolution(res); err != nil {
		return out, err
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("begin resolution for %s: %w", res.CompanySlug, err)
	}
	defer func() { _ = tx.Rollback() }()

	// Read the previous value before writing so the caller can tell an insert from an update. A dry
	// run changes nothing, so every count stays false and the read would be pointless.
	var previous sql.NullString
	if !res.DryRun {
		if err := tx.QueryRowContext(ctx,
			`SELECT career_site_url FROM companies WHERE id = ?`, res.CompanyID).Scan(&previous); err != nil {
			return out, fmt.Errorf("read career_site_url for %s: %w", res.CompanySlug, err)
		}
	}

	for i, a := range res.Attempts {
		index := firstAttemptIndex + i
		// Evidence is written before the row that names it, so a row never points at a file that
		// does not exist. A path collision is reported rather than overwritten: the path is keyed by
		// run and index, so a collision means this run wrote the slot twice.
		if len(a.Body) > 0 && w.dataDir != "" && a.EvidencePath == "" {
			path := EvidencePath(w.dataDir, res.CompanySlug, runID, index, hostOfURL(a.CandidateURL), extensionFor(a.ContentType))
			written, err := WriteEvidence(path, a.Body)
			if err != nil {
				return out, err
			}
			a.EvidencePath = written
		}
		if err := insertAttempt(ctx, tx, runID, res.CompanyID, index, a); err != nil {
			return out, err
		}
		out.AttemptsInserted++
	}

	if !res.DryRun {
		if err := updateCompanyResolution(ctx, tx, res); err != nil {
			return out, err
		}
		if res.ATSPlatformID != 0 {
			if err := upsertApplicationPlatform(ctx, tx, res); err != nil {
				return out, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("commit resolution for %s: %w", res.CompanySlug, err)
	}

	switch {
	case res.DryRun:
		// Nothing changed, so nothing is counted as changed.
	case res.Resolved() && !previous.Valid:
		out.CareerSiteInserted = true
	case res.Resolved() && previous.Valid && previous.String != res.CareerSiteURL:
		out.CareerSiteUpdated = true
	}
	return out, nil
}

// validateResolution rejects a resolution the schema or the vocabularies would refuse, before any
// transaction is opened. Failing here means the caller learns about the mistake from an error
// naming the company rather than from a rollback mid-run.
func validateResolution(res Resolution) error {
	if res.CompanyID == 0 {
		return fmt.Errorf("resolution for %q: company id is zero", res.CompanySlug)
	}
	if res.Resolved() && !res.CareerSiteSource.IsValid() {
		return fmt.Errorf("resolution for %s: career site source %q is not declared", res.CompanySlug, res.CareerSiteSource)
	}
	if !res.Resolved() && res.CareerSiteSource != "" {
		return fmt.Errorf("resolution for %s: source %q set with no career site URL", res.CompanySlug, res.CareerSiteSource)
	}
	if res.Website != "" && !res.WebsiteSource.IsValid() {
		return fmt.Errorf("resolution for %s: website source %q is not declared", res.CompanySlug, res.WebsiteSource)
	}
	if res.Website == "" && res.WebsiteSource != "" {
		return fmt.Errorf("resolution for %s: website source %q set with no website", res.CompanySlug, res.WebsiteSource)
	}
	if res.ATSPlatformID != 0 && res.ATSBaseURL == "" {
		return fmt.Errorf("resolution for %s: ATS platform set with no tenant URL", res.CompanySlug)
	}
	for i, a := range res.Attempts {
		if a.ValidationState == "" {
			return fmt.Errorf("resolution for %s attempt %d: validation state is empty", res.CompanySlug, i)
		}
		if a.ValidationState == careers.StatusAccepted && a.Reason != "" {
			return fmt.Errorf("resolution for %s attempt %d: accepted attempt carries reason %q", res.CompanySlug, i, a.Reason)
		}
		if a.ValidationState != careers.StatusAccepted && a.Reason == "" {
			return fmt.Errorf("resolution for %s attempt %d: %s attempt has no reason", res.CompanySlug, i, a.ValidationState)
		}
	}
	return nil
}

// insertAttempt writes one attempt row.
//
// rejection_reason is stored as the Outcome enum's string form. An undeclared value is a programming
// error rather than data, so it is refused here instead of being written as free text.
func insertAttempt(ctx context.Context, tx *sql.Tx, runID, companyID int64, index int, a ResolutionAttempt) error {
	reason, err := reasonColumn(a)
	if err != nil {
		return err
	}
	var status any
	if a.HTTPStatus != 0 {
		status = a.HTTPStatus
	}

	const q = `
INSERT INTO url_resolution_attempts
    (company_id, run_id, attempt_index, source, candidate_url, candidate_kind,
     http_status, final_url, title, validation_status, rejection_reason, evidence_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	if _, err := tx.ExecContext(ctx, q,
		companyID, runID, index, a.Source, a.CandidateURL, string(a.Kind),
		status, nullable(a.FinalURL), nullable(a.Title), string(a.ValidationState),
		reason, nullable(a.EvidencePath),
	); err != nil {
		return fmt.Errorf("insert resolution attempt %d: %w", index, err)
	}
	return nil
}

// reasonColumn maps an attempt to its rejection_reason column value, enforcing the closed vocabulary.
func reasonColumn(a ResolutionAttempt) (any, error) {
	if a.ValidationState == careers.StatusAccepted {
		return nil, nil
	}
	if !isDeclaredOutcome(a.Reason) {
		// This is the closed-vocabulary test's counterpart at the database boundary: an undeclared
		// value must stop the write rather than become a string nobody can query.
		return nil, fmt.Errorf("rejection reason %q is not a declared Outcome", a.Reason)
	}
	return string(a.Reason), nil
}

// isDeclaredOutcome reports whether r is one of the reasons the gate can produce.
func isDeclaredOutcome(r careers.Reason) bool {
	if r == "" {
		return false
	}
	for _, declared := range careers.AllReasons() {
		if r == declared {
			return true
		}
	}
	// http_<status> is a constructed family rather than a fixed list, so it is matched by prefix.
	return strings.HasPrefix(string(r), "http_")
}

// updateCompanyResolution writes the resolved values back onto the company row.
//
// career_site_url_checked_at moves on every non-skipped resolution, not only on a change: it is the
// "last verified" field, and a weekly pass that re-confirms an unchanged URL still needs the stamp
// to advance. updated_at moves for the same reason.
func updateCompanyResolution(ctx context.Context, tx *sql.Tx, res Resolution) error {
	const q = `
UPDATE companies
   SET website                      = COALESCE(?, website),
       website_source               = COALESCE(?, website_source),
       career_site_url              = COALESCE(?, career_site_url),
       career_site_url_source       = COALESCE(?, career_site_url_source),
       career_site_url_checked_at   = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       career_site_url_http_status  = COALESCE(?, career_site_url_http_status),
       career_site_url_title        = COALESCE(?, career_site_url_title),
       updated_at                   = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id = ?`

	var websiteSource, careerSource any
	if res.WebsiteSource != "" {
		websiteSource = string(res.WebsiteSource)
	}
	if res.CareerSiteSource != "" {
		careerSource = string(res.CareerSiteSource)
	}
	var status any
	if res.CareerSiteStatus != 0 {
		status = res.CareerSiteStatus
	}

	if _, err := tx.ExecContext(ctx, q,
		nullable(res.Website), websiteSource,
		nullable(res.CareerSiteURL), careerSource,
		status, nullable(res.CareerSiteTitle),
		res.CompanyID,
	); err != nil {
		return fmt.Errorf("update company %s: %w", res.CompanySlug, err)
	}
	return nil
}

// upsertApplicationPlatform records the applicant-tracking system behind an accepted board.
//
// The app-enforced invariant from 001_init.sql is checked here: platform_id must reference a row
// whose platform_type is 'application_system'. SQLite has no partial foreign key, so a mistake
// would otherwise link a company to a job board or a reference source.
func upsertApplicationPlatform(ctx context.Context, tx *sql.Tx, res Resolution) error {
	var platformType string
	if err := tx.QueryRowContext(ctx,
		`SELECT platform_type FROM platforms WHERE id = ?`, res.ATSPlatformID).Scan(&platformType); err != nil {
		return fmt.Errorf("read platform %d for %s: %w", res.ATSPlatformID, res.CompanySlug, err)
	}
	if platformType != "application_system" {
		return fmt.Errorf("platform %d for company %s is %q, want application_system",
			res.ATSPlatformID, res.CompanySlug, platformType)
	}

	const q = `
INSERT INTO company_application_platforms (company_id, platform_id, base_url)
VALUES (?, ?, ?)
ON CONFLICT(company_id, platform_id) DO UPDATE SET base_url = excluded.base_url`

	if _, err := tx.ExecContext(ctx, q, res.CompanyID, res.ATSPlatformID, res.ATSBaseURL); err != nil {
		return fmt.Errorf("upsert application platform for %s: %w", res.CompanySlug, err)
	}
	return nil
}

// EvidencePath is the on-disk location of the response body behind one attempt.
//
// The run id is in the path rather than a date. A date would collide on a same-day rerun, and the
// exit-5 convention exists for dated captures; using the run id means a collision can only mean the
// same run wrote the same slot twice, which is a bug worth failing on.
//
//	data/raw/careers/<slug>/<run-id>/<index>-<host>.<ext>
func EvidencePath(dataDir, companySlug string, runID int64, attemptIndex int, host, ext string) string {
	if host == "" {
		host = "unknown"
	}
	return filepath.Join(dataDir, "raw", "careers", safePathPart(companySlug),
		fmt.Sprintf("%d", runID),
		fmt.Sprintf("%d-%s.%s", attemptIndex, safePathPart(host), ext))
}

// WriteEvidence stores an attempt's response body and returns the path.
//
// O_EXCL is used for the same reason the dated captures use it: an occupied slot means the writer
// processed the same run slot twice, and silently overwriting would hide that. The path is returned
// even on collision so the caller can record where the existing evidence is.
func WriteEvidence(path string, body []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, fmt.Errorf("create evidence directory %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return path, ErrEvidenceExists
		}
		return path, fmt.Errorf("create evidence %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(body); err != nil {
		return path, fmt.Errorf("write evidence %s: %w", path, err)
	}
	return path, nil
}

// hostOfURL returns the host of a URL, or empty when it cannot be parsed.
func hostOfURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Hostname()
}

// extensionFor picks the evidence file's extension from its content type.
func extensionFor(contentType string) string {
	if strings.Contains(strings.ToLower(contentType), "json") {
		return "json"
	}
	return "html"
}

// safePathPart reduces a host or slug to characters that are safe in a single path element.
//
// A host cannot contain a separator, but a slug is derived from a company name and a mistake in that
// derivation must not be able to write outside the evidence directory.
func safePathPart(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" || out == "." || out == ".." {
		return "unknown"
	}
	return out
}
