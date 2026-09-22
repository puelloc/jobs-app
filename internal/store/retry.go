// retry.go: SQLite lock detection and the single bounded retry that the design
// doc's failure policy allows for the "DB is locked" condition.
//
// design: Failure policy ("DB is locked") and Non-goals ("no retries beyond ...").
package store

import (
	"context"
	"errors"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// lockRetryDelay is how long to wait before the one permitted retry.
//
// It is a variable rather than a constant so tests can shorten the wait; the
// production value never changes at runtime.
var lockRetryDelay = time.Second

// IsLockError reports whether err is a SQLite write-contention error
// (SQLITE_BUSY / SQLITE_LOCKED and their extended forms), as opposed to a
// schema, constraint, or programming error. Callers use this to choose exit
// code 3 rather than a generic failure.
//
// Extended result codes share the low byte with their primary code, so the
// primary code is recovered by masking.
func IsLockError(err error) bool {
	if err == nil {
		return false
	}
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	switch serr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	default:
		return false
	}
}

// RunWithOneRetry runs a single statement or statement-producing call, retrying
// it once after a short delay if the database is locked. At most one retry is
// attempted per failing statement: a second lock failure is returned to the
// caller, which maps it to exit code 3.
//
// design: Failure policy ("DB is locked") and Non-goals ("no retries beyond ...").
func RunWithOneRetry(ctx context.Context, op func() error) error {
	err := op()
	if !IsLockError(err) {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(lockRetryDelay):
	}

	return op()
}
