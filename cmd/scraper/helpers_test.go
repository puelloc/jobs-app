// helpers_test.go: capture helpers and a real-database fixture for main's tests.
package main

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"jobsapp/internal/db"
	"jobsapp/internal/store"
)

// captureOutput runs fn with os.Stdout and os.Stderr redirected and returns what
// each received. The scraper's entire observable contract is these two streams,
// so tests assert on them directly.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}

	os.Stdout, os.Stderr = outW, errW

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()

	func() {
		defer func() {
			os.Stdout, os.Stderr = origOut, origErr
			_ = outW.Close()
			_ = errW.Close()
		}()
		fn()
	}()

	stdout = <-outCh
	stderr = <-errCh
	_ = outR.Close()
	_ = errR.Close()
	return stdout, stderr
}

// openTestDB opens a migrated database in a temp dir, returning the pool and its
// file path. The pool is capped at one connection (db.Open sets
// MaxOpenConns(1)), so a test that needs a concurrent writer must open a second
// pool against the same path rather than asking this one for another connection.
func openTestDB(t *testing.T) (*sql.DB, string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		return nil, "", err
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, path, nil
}

// realBusyError produces a genuine SQLITE_BUSY. The lock is released before this
// returns, so the caller can go on to run more SQL. The driver's error type has
// unexported fields, so a real lock is the only way to obtain one.
func realBusyError(t *testing.T, database *sql.DB, dbPath string) error {
	t.Helper()

	holder, err := database.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire holder conn: %v", err)
	}
	defer holder.Close()

	if _, err := holder.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}

	// A second, independent pool: the pool under test has exactly one connection
	// and is holding it above.
	side, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatalf("open side pool: %v", err)
	}
	defer side.Close()

	conn, err := side.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire side conn: %v", err)
	}
	defer conn.Close()

	_, lockErr := conn.ExecContext(context.Background(),
		`INSERT INTO platforms (id, name, platform_type) VALUES (9002, 'busy-main-test', 'job_board')`)

	// Release before returning so subsequent statements in the test are not
	// blocked by this connection.
	if _, err := holder.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatalf("rollback holder: %v", err)
	}

	if lockErr == nil {
		t.Fatal("expected a lock error, but the concurrent write succeeded")
	}
	if !store.IsLockError(lockErr) {
		t.Fatalf("concurrent write failed with %v, which is not classified as a lock error", lockErr)
	}
	return lockErr
}
