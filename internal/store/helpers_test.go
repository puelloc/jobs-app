// helpers_test.go: small shared test helpers.
package store

import "fmt"

// errorString is a minimal non-SQLite error, used to prove that IsLockError and
// RunWithOneRetry do not treat arbitrary errors as lock contention.
type errorString string

func (e errorString) Error() string { return string(e) }

// wrapError wraps an error the way callers do, so tests can prove that
// errors.As still finds the underlying driver error inside a wrapper.
func wrapError(err error) error {
	return fmt.Errorf("wrapped: %w", err)
}
