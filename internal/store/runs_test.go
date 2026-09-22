// runs_test.go: tests for run bookkeeping against a real SQLite file.
package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jobsapp/internal/db"
)

// newTestDB opens a migrated database in a temp dir. Because it goes through
// db.Open, the schema, seed rows, and DSN pragmas are the real ones.
//
// The returned pool is capped at one connection (db.Open sets MaxOpenConns(1)),
// so tests that need concurrency must open a second pool rather than asking this
// one for another connection.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, _ := newTestDBAt(t)
	return database
}

// newTestDBAt also returns the database file path, for tests that need a second
// independent pool against the same file.
func newTestDBAt(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, path
}

func TestStartRunInsertsRunningRow(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	before := time.Now().UTC().Add(-2 * time.Second)
	id, startedAt, err := StartRun(ctx, database, 1)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want a positive rowid", id)
	}
	if startedAt.IsZero() {
		t.Fatal("startedAt is the zero time, want the value the database generated")
	}
	if startedAt.Before(before) || startedAt.After(time.Now().UTC().Add(2*time.Second)) {
		t.Errorf("startedAt = %s, want it near now", startedAt)
	}

	var status string
	var finished sql.NullString
	var found, inserted, updated int64
	var errText sql.NullString
	if err := database.QueryRowContext(ctx,
		`SELECT status, finished_at, items_found, items_inserted, items_updated, error_text
		   FROM scrape_runs WHERE id = ?`, id).
		Scan(&status, &finished, &found, &inserted, &updated, &errText); err != nil {
		t.Fatalf("read back run: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want running", status)
	}
	if finished.Valid {
		t.Errorf("finished_at = %q, want NULL for an in-flight run", finished.String)
	}
	if found != 0 || inserted != 0 || updated != 0 {
		t.Errorf("counters = %d/%d/%d, want all zero", found, inserted, updated)
	}
	if errText.Valid {
		t.Errorf("error_text = %q, want NULL", errText.String)
	}
}

// design: Run lifecycle step 3 - the row must be committed before the network is
// touched, so it is visible to a different connection immediately.
func TestStartRunIsVisibleToOtherConnections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	writer, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer writer.Close()

	id, _, err := StartRun(context.Background(), writer, 1)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	reader, err := db.Open(path)
	if err != nil {
		t.Fatalf("second db.Open: %v", err)
	}
	defer reader.Close()

	var got int64
	if err := reader.QueryRow(`SELECT id FROM scrape_runs WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatalf("run %d not visible to a second connection: %v", id, err)
	}
}

func TestStartRunRejectsUnknownPlatform(t *testing.T) {
	database := newTestDB(t)
	if _, _, err := StartRun(context.Background(), database, 9999); err == nil {
		t.Fatal("StartRun accepted a platform_id with no platforms row; want an FK violation")
	}
}

func TestFinishRunWritesTerminalState(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	id, _, err := StartRun(ctx, database, 1)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	msg := "no_jobs: no job elements accepted"
	if err := FinishRun(ctx, database, id, "error", 100, 0, 0, &msg); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	var status, errText string
	var finished string
	var found, inserted, updated int64
	if err := database.QueryRowContext(ctx,
		`SELECT status, finished_at, items_found, items_inserted, items_updated, error_text
		   FROM scrape_runs WHERE id = ?`, id).
		Scan(&status, &finished, &found, &inserted, &updated, &errText); err != nil {
		t.Fatalf("read back run: %v", err)
	}
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if errText != msg {
		t.Errorf("error_text = %q, want %q", errText, msg)
	}
	if found != 100 || inserted != 0 || updated != 0 {
		t.Errorf("counters = %d/%d/%d, want 100/0/0", found, inserted, updated)
	}
	// finished_at must be the schema's RFC3339 UTC shape.
	ts, err := time.Parse(time.RFC3339, finished)
	if err != nil {
		t.Fatalf("finished_at = %q, which does not parse as RFC3339: %v", finished, err)
	}
	if !strings.HasSuffix(finished, "Z") {
		t.Errorf("finished_at = %q, want a Z suffix (UTC)", finished)
	}
	if ts.Location() != time.UTC {
		t.Errorf("finished_at location = %s, want UTC", ts.Location())
	}
}

func TestFinishRunAcceptsNilErrorText(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	id, _, err := StartRun(ctx, database, 1)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := FinishRun(ctx, database, id, "ok", 99, 99, 0, nil); err != nil {
		t.Fatalf("FinishRun with nil error text: %v", err)
	}

	var errText sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT error_text FROM scrape_runs WHERE id = ?`, id).Scan(&errText); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if errText.Valid {
		t.Errorf("error_text = %q, want NULL on success", errText.String)
	}
}

// design: the status CHECK is enforced by the schema, not re-validated in Go.
func TestFinishRunReliesOnSchemaCheckForStatus(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	id, _, err := StartRun(ctx, database, 1)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := FinishRun(ctx, database, id, "not-a-status", 0, 0, 0, nil); err == nil {
		t.Fatal("FinishRun accepted an invalid status; want the schema CHECK to reject it")
	}
}

func TestFinishRunUnknownIDIsNotAnError(t *testing.T) {
	// An UPDATE matching no rows is not an error in SQL; this pins that behavior
	// so a future change to check RowsAffected is deliberate.
	database := newTestDB(t)
	if err := FinishRun(context.Background(), database, 987654, "error", 0, 0, 0, nil); err != nil {
		t.Fatalf("FinishRun on a missing id returned %v, want nil", err)
	}
}

// --- retry policy --------------------------------------------------------

func TestRunWithOneRetryDoesNotRetrySuccess(t *testing.T) {
	calls := 0
	err := RunWithOneRetry(context.Background(), func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithOneRetry: %v", err)
	}
	if calls != 1 {
		t.Errorf("op ran %d times, want 1", calls)
	}
}

func TestRunWithOneRetryDoesNotRetryOtherErrors(t *testing.T) {
	sentinel := errorString("boom")
	calls := 0
	err := RunWithOneRetry(context.Background(), func() error {
		calls++
		return sentinel
	})
	if err != sentinel {
		t.Errorf("err = %v, want the original error", err)
	}
	if calls != 1 {
		t.Errorf("op ran %d times, want 1 (only lock errors are retried)", calls)
	}
}

func TestRunWithOneRetryRetriesALockOnce(t *testing.T) {
	database, path := newTestDBAt(t)
	useFastRetryDelay(t)

	lockErr, release := acquireLockError(t, database, path)
	defer release()

	calls := 0
	err := RunWithOneRetry(context.Background(), func() error {
		calls++
		if calls == 1 {
			return lockErr
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithOneRetry did not recover from the first lock: %v", err)
	}
	if calls != 2 {
		t.Errorf("op ran %d times, want 2 (one retry)", calls)
	}
}

func TestRunWithOneRetryGivesUpAfterOneRetry(t *testing.T) {
	database, path := newTestDBAt(t)
	useFastRetryDelay(t)

	lockErr, release := acquireLockError(t, database, path)
	defer release()

	calls := 0
	err := RunWithOneRetry(context.Background(), func() error {
		calls++
		return lockErr
	})
	if !IsLockError(err) {
		t.Errorf("err = %v, want the lock error to surface", err)
	}
	if calls != 2 {
		t.Errorf("op ran %d times, want exactly 2 (no second retry)", calls)
	}
}

func TestRunWithOneRetryHonoursCancelledContext(t *testing.T) {
	database, path := newTestDBAt(t)

	lockErr, release := acquireLockError(t, database, path)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	err := RunWithOneRetry(ctx, func() error {
		calls++
		return lockErr
	})
	if err == nil {
		t.Fatal("want an error from a cancelled context")
	}
	if calls != 1 {
		t.Errorf("op ran %d times, want 1 (no retry after cancellation)", calls)
	}
}

func TestIsLockError(t *testing.T) {
	database, path := newTestDBAt(t)

	lockErr, release := acquireLockError(t, database, path)
	defer release()

	if IsLockError(nil) {
		t.Error("IsLockError(nil) = true, want false")
	}
	if IsLockError(errorString("not a sqlite error")) {
		t.Error("IsLockError(plain error) = true, want false")
	}
	if !IsLockError(lockErr) {
		t.Errorf("IsLockError(%v) = false, want true for a real SQLITE_BUSY", lockErr)
	}
	// Wrapped errors must still be recognized.
	if !IsLockError(wrapError(lockErr)) {
		t.Error("IsLockError(wrapped real lock error) = false, want true")
	}
}

// --- helpers -------------------------------------------------------------

// useFastRetryDelay removes the production 1s retry wait from the test path.
func useFastRetryDelay(t *testing.T) {
	t.Helper()
	orig := lockRetryDelay
	lockRetryDelay = time.Millisecond
	t.Cleanup(func() { lockRetryDelay = orig })
}

// acquireLockError produces a genuine SQLITE_BUSY and returns it together with a
// function that releases the write lock.
//
// The lock is held until release is called. A SECOND POOL is used because the
// pool under test is capped at one connection (design: db.Open sets
// MaxOpenConns(1)); asking that pool for a second connection while the first is
// checked out would deadlock. The driver's error type has unexported fields, so
// a real lock is the only way to obtain one.
func acquireLockError(t *testing.T, database *sql.DB, dbPath string) (error, func()) {
	t.Helper()
	holder, err := database.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire holder conn: %v", err)
	}
	if _, err := holder.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		_ = holder.Close()
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}

	var once bool
	release := func() {
		if once {
			return
		}
		once = true
		_, _ = holder.ExecContext(context.Background(), `ROLLBACK`)
		_ = holder.Close()
	}
	t.Cleanup(release)

	// Probe with an independent pool so the single-connection pool stays free.
	// The DSN sets busy_timeout(1) so the write fails immediately rather than
	// waiting out the production 5s timeout.
	side, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(1)")
	if err != nil {
		release()
		t.Fatalf("open side pool: %v", err)
	}
	defer side.Close()

	conn, err := side.Conn(context.Background())
	if err != nil {
		release()
		t.Fatalf("acquire side conn: %v", err)
	}
	defer conn.Close()

	_, lockErr := conn.ExecContext(context.Background(),
		`INSERT INTO platforms (id, name, platform_type) VALUES (9001, 'lock-test', 'job_board')`)
	if lockErr == nil {
		release()
		t.Fatal("expected a lock error, but the concurrent write succeeded")
	}
	if !IsLockError(lockErr) {
		release()
		t.Fatalf("concurrent write failed with %v, which is not classified as a lock error", lockErr)
	}
	return lockErr, release
}
