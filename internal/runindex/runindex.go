// Package runindex drives one S&P index page from fetch to database.
//
// It is the lifecycle layer: fetch, persist the raw bytes before parsing, parse, upsert, and record
// a scrape_runs row. The HTTP client is injected so the whole path is testable without touching the
// network, and so a caller can substitute a recording client.
//
// design: docs/sp1500-plan.md - M1. The pages are an identity source: they say who a company is,
// not whether it hires.
package runindex

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"time"

	"jobsapp/internal/rawstore"
	"jobsapp/internal/sp1500"
	"jobsapp/internal/store"
)

// Exit codes, matching the documented scheme. They are distinct values rather than an error type
// because the command exits with them.
const (
	ExitOK           = 0
	ExitFailure      = 1 // network, parse, or database failure
	ExitConfig       = 2 // bad configuration or migration failure
	ExitLock         = 3 // database write contention
	ExitEmpty        = 4 // the page parsed but yielded no rows
	ExitRawCollision = 5 // the run succeeded, but this UTC day already had a capture
)

// Result reports what a completed run did.
type Result struct {
	RunID     int64
	Source    string
	Index     sp1500.Index
	StartedAt time.Time
	RawPath   string
	RawState  string // "written" or "existing"
	Found     int
	Inserted  int
	Duration  time.Duration
	ExitCode  int
}

// Client is the subset of *http.Client the runner needs. It is an interface so a test can drive the
// whole lifecycle without opening a socket; *http.Client satisfies it.
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// Runner executes one index page's run.
type Runner struct {
	DB       *sql.DB
	Client   Client
	DataDir  string
	URL      string
	Index    sp1500.Index
	Source   string
	Platform int64
	// Now is injectable so a test can pin the UTC capture date. Defaults to time.Now.
	Now func() time.Time
}

// Run performs the whole lifecycle and returns a Result. An error is returned alongside a non-zero
// ExitCode; the Result is populated even on failure so the caller can report what was observed.
func (r Runner) Run(ctx context.Context) (Result, error) {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	res := Result{Source: r.Source, Index: r.Index, StartedAt: now()}

	runID, startedAt, err := store.StartRun(ctx, r.DB, r.Platform)
	if err != nil {
		res.ExitCode = ExitFailure
		return res, fmt.Errorf("start run: %w", err)
	}
	res.RunID = runID
	// started_at comes from the database rather than the injected clock, so the recorded run
	// time is the one the row actually holds.
	res.StartedAt = startedAt

	body, err := r.fetch(ctx)
	if err != nil {
		failErr := r.fail(ctx, runID, err)
		res.ExitCode = ExitFailure
		return res, failErr
	}

	parsed, err := sp1500.Parse(r.Index, string(body))
	if err != nil {
		failErr := r.fail(ctx, runID, err)
		res.ExitCode = ExitFailure
		// The raw payload is written before parsing precisely so this is recoverable: the fix
		// is a re-parse, not a re-fetch.
		path, _, writeErr := rawstore.Write(r.DataDir, r.Source, "wiki", res.StartedAt, body)
		if writeErr == nil {
			res.RawPath = path
		}
		return res, failErr
	}

	path, written, err := rawstore.Write(r.DataDir, r.Source, "wiki", res.StartedAt, body)
	if err != nil {
		failErr := r.fail(ctx, runID, err)
		res.ExitCode = ExitFailure
		return res, failErr
	}
	res.RawPath = path
	if written {
		res.RawState = "written"
	} else {
		res.RawState = "existing"
	}

	res.Found = len(parsed.Companies)

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		failErr := r.fail(ctx, runID, err)
		res.ExitCode = ExitFailure
		return res, failErr
	}
	defer func() { _ = tx.Rollback() }()

	companies := make([]store.IndexCompany, 0, len(parsed.Companies))
	for _, c := range parsed.Companies {
		companies = append(companies, store.IndexCompany{
			Name:         c.Name,
			Article:      c.Article,
			Industry:     c.Industry,
			SubIndustry:  c.SubIndustry,
			Headquarters: c.Headquarters,
			CIK:          c.CIK,
			RawCIK:       c.RawCIK,
		})
	}
	inserted, err := store.UpsertIndexCompanies(ctx, tx, string(r.Index), companies)
	if err != nil {
		failErr := r.fail(ctx, runID, err)
		res.ExitCode = ExitFailure
		return res, failErr
	}
	if err := tx.Commit(); err != nil {
		failErr := r.fail(ctx, runID, err)
		res.ExitCode = ExitFailure
		return res, failErr
	}
	res.Inserted = inserted

	if err := store.FinishRun(ctx, r.DB, runID, "ok", int64(res.Found), int64(res.Inserted), 0, nil); err != nil {
		res.ExitCode = ExitFailure
		return res, fmt.Errorf("finish run: %w", err)
	}
	res.Duration = time.Since(res.StartedAt)

	// A page that parses to nothing is not a success: an empty feed must not look like a quiet
	// day, and the run is already recorded as ok because it genuinely ran.
	if res.Found == 0 {
		res.ExitCode = ExitEmpty
		return res, nil
	}
	if !written {
		res.ExitCode = ExitRawCollision
		return res, nil
	}
	res.ExitCode = ExitOK
	return res, nil
}

// fetch performs the request and returns the body. A non-2xx status is an error: the raw capture
// would otherwise record an error page as though it were the page.
func (r Runner) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", r.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("fetch %s: unexpected status %d", r.URL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body from %s: %w", r.URL, err)
	}
	return body, nil
}

// fail records the failure on the run row and returns the original error, so the caller sees the
// cause rather than the bookkeeping error.
func (r Runner) fail(ctx context.Context, runID int64, cause error) error {
	msg := cause.Error()
	if err := store.FinishRun(ctx, r.DB, runID, "error", 0, 0, 0, &msg); err != nil {
		return fmt.Errorf("%w (also failed to record the run: %v)", cause, err)
	}
	return cause
}
