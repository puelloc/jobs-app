// Command scraper performs one RemoteOK scrape run and exits.
//
// It implements the lifecycle in docs/scraper-design.md steps 1-13. One step is
// deliberately stubbed: normalization (step 7) accepts zero jobs until
// docs/remoteok-mapping.md exists, so a run ends on the documented "no usable job
// elements" path with exit code 4. Every other step - config, DB open and
// migrate, scrape_runs insert, fetch, raw persist, parse, both stale-marking
// guards, run finish, and the stderr failure line - is real.
//
// There is no scheduler, no server, and no logger: logs go to stdout/stderr.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/scraper/remoteok"
	"jobsapp/internal/store"
)

const (
	// platformID is the 'remoteok' job_board seed id from
	// internal/db/migrations/002_seed_platforms.sql.
	// design: Run lifecycle step 3.
	platformID int64 = 1

	sourceName = "remoteok"
)

// Exit codes, per the design doc's Failure policy table.
// design: Failure policy / "Exit codes".
const (
	exitOK           = 0
	exitFailure      = 1 // network, protocol, parse, normalization
	exitConfig       = 2 // config, migration, startup, or raw-write failure
	exitDBLocked     = 3 // database write contention
	exitNoJobs       = 4 // valid response with no usable job elements
	exitRawCollision = 5 // run succeeded, raw capture for this date already existed
)

// finishTimeout bounds the terminal scrape_runs write, which must not be
// cancelled by the request deadline that ended the fetch.
const finishTimeout = 5 * time.Second

func main() {
	os.Exit(run(context.Background()))
}

func run(ctx context.Context) int {
	// ---- step 1: load config (no I/O) -------------------------------------
	cfg, err := config.Load()
	if err != nil {
		// design: Run lifecycle step 1 - exits here with no trace in the database.
		printFailure("none", failure{step: "config", condition: "bad_config", err: err}, exitConfig)
		return exitConfig
	}

	// design: Inputs - HTTP_TIMEOUT is a whole-request deadline, not per-connection.
	ctx, cancel := context.WithTimeout(ctx, cfg.HTTPTimeout)
	defer cancel()
	runStart := time.Now()

	// ---- step 2: open DB (pragmas + migrations) ---------------------------
	// design: Run lifecycle step 2.
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		// design: Failure policy - "Migration fails on startup": no scrape_runs
		// row exists at this point, and the network is never touched.
		printFailure("none", failure{step: "open", condition: "db_open_failed", err: err}, exitConfig)
		return exitConfig
	}
	defer func() {
		if cerr := database.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "warn=close_failed err=%q\n", singleLine(cerr.Error()))
		}
	}()

	// ---- step 3: insert scrape_runs row, committed before the network -----
	// design: Run lifecycle step 3, and Failure policy - "DB is locked".
	var (
		runID     int64
		startedAt time.Time
	)
	err = store.RunWithOneRetry(ctx, func() error {
		var startErr error
		runID, startedAt, startErr = store.StartRun(ctx, database, platformID)
		return startErr
	})
	if err != nil {
		if store.IsLockError(err) {
			printFailure("none", failure{step: "open", condition: "db_locked", err: err}, exitDBLocked)
			return exitDBLocked
		}
		printFailure("none", failure{step: "open", condition: "run_insert_failed", err: err}, exitConfig)
		return exitConfig
	}
	runLabel := fmt.Sprintf("%d", runID)

	// ---- step 4: fetch ----------------------------------------------------
	// design: Run lifecycle step 4; Failure policy for DNS/TLS/403/404/429/5xx/timeout.
	body, httpStatus, err := remoteok.Fetch(ctx, cfg.Endpoint, cfg.UserAgent, cfg.HTTPTimeout)
	if err != nil {
		return finishFailed(database, runID, finishInput{
			itemsFound: 0,
			failure:    failure{step: "fetch", condition: fetchCondition(err, httpStatus), err: err, httpCode: httpStatus},
			exitCode:   exitFailure,
		})
	}

	// ---- step 5: persist raw payload, BEFORE any parsing ------------------
	// design: Run lifecycle step 5 - a parse bug must never cost us the bytes.
	rawPath, rawWritten, err := writeRawPayload(cfg.DataDir, startedAt, body)
	if err != nil {
		return finishFailed(database, runID, finishInput{
			failure:  failure{step: "raw", condition: "raw_write_failed", err: err, httpCode: httpStatus},
			exitCode: exitConfig,
		})
	}

	// ---- step 6: parse ----------------------------------------------------
	// design: Run lifecycle step 6. The raw file is already on disk, which is what
	// makes a parse failure recoverable without a re-fetch.
	elements, err := remoteok.Parse(body)
	if err != nil {
		return finishFailed(database, runID, finishInput{
			failure: failure{
				step: "parse", condition: "invalid_json", err: err,
				httpCode: httpStatus, rawPath: rawPath,
			},
			exitCode: exitFailure,
		})
	}

	// design: scrape_runs.items_found counts raw elements. The real normalizer
	// will exclude a detected legal notice; the stub inspects nothing, so every
	// element is counted.
	itemsFound := int64(len(elements))

	// ---- step 7: extract identifiers, then normalize (STUB) ---------------
	// design: Run lifecycle step 7. Normalize keeps the real signature but has a
	// stubbed body: no jobs, an empty seen set, one skip per element.
	jobs, seenIDs, skipped, err := remoteok.Normalize(elements)
	if err != nil {
		return finishFailed(database, runID, finishInput{
			itemsFound: itemsFound,
			failure:    failure{step: "normalize", condition: "normalize_failed", err: err, httpCode: httpStatus, rawPath: rawPath},
			exitCode:   exitFailure,
		})
	}

	// ---- steps 8 and 9: upsert jobs, then mark stale jobs -----------------
	// design: Run lifecycle steps 8 and 9. Both are no-ops this iteration: with
	// zero accepted jobs there is nothing to upsert, and BOTH stale-marking guards
	// fire - the seen set is empty and the accepted count is zero - so no row is
	// closed. This is a documented path, not a shortcut.
	//
	// The upsert counters stay zero for the whole of this iteration: the stub
	// accepts no jobs, so the optional upsert step below is unreachable in
	// practice, and the zero-jobs path records 0/0/0 by construction.
	if len(jobs) > 0 {
		// design-gap: steps 8-10 need a jobs store (internal/store/jobs.go), which
		// the build spec excludes until docs/remoteok-mapping.md exists. Reaching
		// here is impossible while Normalize is stubbed; the branch exists so the
		// flow does not silently pretend the steps ran.
		return finishFailed(database, runID, finishInput{
			itemsFound: itemsFound,
			failure: failure{
				step: "upsert", condition: "not_implemented",
				err:      fmt.Errorf("job upsert is not implemented"),
				httpCode: httpStatus, rawPath: rawPath,
			},
			exitCode: exitFailure,
		})
	}

	// Per-element skip reasons go to stderr only at debug level.
	//
	// design: Observability - a skipped row gets its own line at debug level. The
	// design doc also permits promoting those lines to info in the
	// "nothing normalized" case, but this build's spec requires exactly ONE stderr
	// line per run, so the promotion is off by default and the aggregate count
	// lives in the single failure line instead. LOG_LEVEL=debug turns the detail
	// on; the run's failure line is unaffected either way.
	if cfg.LogLevel == "debug" {
		for _, sk := range skipped {
			fmt.Fprintf(os.Stderr, "run=%s source=%s level=debug step=normalize skipped_index=%d reason=%q\n",
				runLabel, sourceName, sk.Index, sk.Reason)
		}
	}

	if len(seenIDs) == 0 {
		// design: Failure policy rows 11-12 - zero usable job elements: keep the
		// raw file, skip stale-marking, finish as an error, exit 4.
		f := failure{
			step: "normalize", condition: "no_jobs",
			err:      fmt.Errorf("no job elements accepted from %d parsed element(s)", len(elements)),
			httpCode: httpStatus, rawPath: rawPath,
		}

		if ferr := finishRunRow(database, runID, "error", itemsFound, 0, 0, errorText(f)); ferr != nil {
			return finishWriteFailed(runLabel, ferr, httpStatus, rawPath)
		}

		if rawWritten {
			printFailure(runLabel, f, exitNoJobs)
			return exitNoJobs
		}

		// design: Durability / "Same-day collision behavior" - the run succeeded;
		// the capture on disk is an earlier one, so print the success line with
		// raw=existing and exit 5.
		printSuccess(runID, httpStatus, len(body), rawPath, "existing", itemsFound, 0, 0, 0, time.Since(runStart))
		return exitRawCollision
	}

	// Identifiers observed but nothing normalized: do not close anything, and do
	// not report success.
	// design: Failure policy row 13.
	return finishFailed(database, runID, finishInput{
		itemsFound: itemsFound,
		failure: failure{
			step: "normalize", condition: "normalize_failed",
			err:      fmt.Errorf("observed %d identifier(s) but accepted no jobs", len(seenIDs)),
			httpCode: httpStatus, rawPath: rawPath,
		},
		exitCode: exitFailure,
	})
}

// finishInput carries the terminal state of a failed run.
type finishInput struct {
	itemsFound int64
	failure    failure
	exitCode   int
}

// finishFailed records a failed run, prints the single stderr line, and returns
// the exit code.
//
// design: Observability / stdout-stderr on failure; Failure policy - every
// condition that got past step 3 finishes the run row.
func finishFailed(database *sql.DB, runID int64, in finishInput) int {
	runLabel := fmt.Sprintf("%d", runID)
	if err := finishRunRow(database, runID, "error", in.itemsFound, 0, 0, errorText(in.failure)); err != nil {
		return finishWriteFailed(runLabel, err, in.failure.httpCode, in.failure.rawPath)
	}
	printFailure(runLabel, in.failure, in.exitCode)
	return in.exitCode
}

// finishRunRow writes the terminal scrape_runs state. It uses its own bounded
// context because the request deadline may already have expired.
// design: Run lifecycle step 11.
func finishRunRow(database *sql.DB, runID int64, status string, found, inserted, updated int64, errText *string) error {
	ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	return store.FinishRun(ctx, database, runID, status, found, inserted, updated, errText)
}

// finishWriteFailed handles the case where even the terminal write failed: the
// row stays 'running', which is the documented marker of an abandoned run.
// design: Failure policy - "DB is locked" and the 'running' row consequence.
func finishWriteFailed(runLabel string, err error, httpStatus int, rawPath string) int {
	f := failure{step: "finish", condition: "finish_failed", err: err, httpCode: httpStatus, rawPath: rawPath}
	exitCode := exitConfig
	if store.IsLockError(err) {
		exitCode = exitDBLocked
	}
	printFailure(runLabel, f, exitCode)
	return exitCode
}

// failure is one failed run: where it died, why, and what the operator sees.
type failure struct {
	step      string // config|open|fetch|raw|parse|normalize|upsert|stale|finish
	condition string // short, log-safe label
	err       error
	httpCode  int
	rawPath   string
}

// writeRawPayload persists the response bytes verbatim to
// $DATA_DIR/raw/remoteok-YYYY-MM-DD.json, the date taken from the run's
// started_at in UTC. It reports written=false when a capture for that date
// already exists - the existing file is never truncated or replaced.
//
// design: Durability - one capture per UTC date, O_CREATE|O_EXCL, and a
// temp-file-then-rename so a crash cannot leave a truncated capture behind.
func writeRawPayload(dataDir string, startedAt time.Time, body []byte) (path string, written bool, err error) {
	rawDir := filepath.Join(dataDir, "raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return "", false, fmt.Errorf("create raw directory %s: %w", rawDir, err)
	}

	finalPath := filepath.Join(rawDir, rawFilename(startedAt))

	// O_EXCL makes the first run of a UTC day authoritative.
	reservation, err := os.OpenFile(finalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return finalPath, false, nil
		}
		return "", false, fmt.Errorf("create %s: %w", finalPath, err)
	}
	if err := reservation.Close(); err != nil {
		return "", false, fmt.Errorf("close reservation %s: %w", finalPath, err)
	}

	// Stage in a temp file in the same directory, then rename into place.
	tmp, err := os.CreateTemp(rawDir, ".remoteok-*.json.tmp")
	if err != nil {
		_ = os.Remove(finalPath)
		return "", false, fmt.Errorf("create temp file in %s: %w", rawDir, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		_ = os.Remove(finalPath)
		return "", false, fmt.Errorf("write payload: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		_ = os.Remove(finalPath)
		return "", false, fmt.Errorf("sync payload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		_ = os.Remove(finalPath)
		return "", false, fmt.Errorf("close temp payload: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		_ = os.Remove(tmpName)
		_ = os.Remove(finalPath)
		return "", false, fmt.Errorf("rename payload into place: %w", err)
	}
	return finalPath, true, nil
}

// rawFilename is the documented capture name: source plus the run's UTC date.
// design: Durability - "$DATA_DIR/raw/remoteok-YYYY-MM-DD.json".
func rawFilename(startedAt time.Time) string {
	return fmt.Sprintf("%s-%s.json", sourceName, startedAt.UTC().Format("2006-01-02"))
}

// printSuccess writes the one stdout line for a completed run, in the documented
// key order.
// design: Observability / "stdout on success".
func printSuccess(runID int64, httpStatus, bytes int, rawPath, rawState string, found, inserted, updated, closed int64, d time.Duration) {
	fmt.Printf("run=%d source=%s status=ok http=%d bytes=%d path=%s raw=%s found=%d inserted=%d updated=%d closed=%d duration_ms=%d\n",
		runID, sourceName, httpStatus, bytes, rawPath, rawState, found, inserted, updated, closed, d.Milliseconds())
}

// printFailure writes exactly one stderr line for a run that did not succeed.
// design: Observability / stdout-stderr on failure.
func printFailure(runLabel string, f failure, exitCode int) {
	http := "-"
	if f.httpCode > 0 {
		http = fmt.Sprintf("%d", f.httpCode)
	}
	rawPath := f.rawPath
	if rawPath == "" {
		rawPath = "-"
	}
	errText := "-"
	if f.err != nil {
		errText = singleLine(f.err.Error())
	}
	fmt.Fprintf(os.Stderr, "run=%s source=%s status=error step=%s condition=%s http=%s err=%q path=%s exit=%d\n",
		runLabel, sourceName, f.step, f.condition, http, errText, rawPath, exitCode)
}

// singleLine collapses newlines and truncates to 1000 characters so one run
// produces exactly one log line.
// design: Observability - "single-line (newlines escaped), truncated to 1000".
func singleLine(s string) string {
	const max = 1000
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if r < 0x20 || r == 0x7f {
				out = append(out, ' ')
				continue
			}
			out = append(out, r)
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return string(out)
}

// errorText builds scrape_runs.error_text: the condition label plus the
// truncated message, matching the stderr line. Never a response body or job text.
// design: Observability / scrape_runs fields - error_text; Not logged.
func errorText(f failure) *string {
	if f.err == nil {
		if f.condition == "" {
			return nil
		}
		s := f.condition
		return &s
	}
	s := f.condition + ": " + singleLine(f.err.Error())
	return &s
}

// fetchCondition maps a fetch failure to a short, log-safe label.
// design: Failure policy - DNS, TLS, 403, 404, 429, 5xx, timeout.
func fetchCondition(err error, httpStatus int) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, remoteok.ErrRetryAfterTooLong):
		return "retry_after_too_long"
	case errors.Is(err, remoteok.ErrUnexpectedStatus):
		switch httpStatus {
		case 403:
			return "http_403"
		case 404:
			return "http_404"
		case 429:
			return "http_429_twice"
		default:
			if httpStatus >= 500 {
				return "http_5xx"
			}
			return "http_status"
		}
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return "dns_failure"
		}
		// The Go HTTP client reports certificate problems as *tls.Certificate-
		// VerificationError; matching on the message avoids importing crypto/tls
		// just for a label.
		if msg := err.Error(); strings.Contains(msg, "tls:") || strings.Contains(msg, "certificate") {
			return "tls_failure"
		}
		return "connection_failed"
	}
}
