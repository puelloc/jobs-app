// client.go: the HTTP fetch and JSON parse steps for RemoteOK.
//
// design: Run lifecycle steps 4 and 6, and the Failure policy table.
package remoteok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	// DefaultEndpoint is the seed value for platform id 1 in
	// internal/db/migrations/002_seed_platforms.sql.
	// TODO(mapping): confirm this URL serves JSON rather than an HTML page.
	DefaultEndpoint = "https://remoteok.com/remote-dev-jobs"

	// DefaultUserAgent identifies this scraper to the source.
	DefaultUserAgent = "jobs-app/0.1 (+https://github.com/local/jobs-app)"

	// DefaultTimeout is the whole-request deadline when HTTP_TIMEOUT is unset.
	DefaultTimeout = 30 * time.Second

	// RetryAfterCap bounds how long the single 429 retry may sleep.
	// design: Failure policy - a larger Retry-After is a permanent failure, not a
	// multi-hour sleep holding a 'running' scrape_runs row open.
	RetryAfterCap = 60 * time.Second

	// defaultRetrySleep is used when the 429 carries no usable Retry-After header.
	// design: Failure policy - "else 30s".
	defaultRetrySleep = 30 * time.Second

	// maxErrorBodyBytes bounds how much of an error response is read so the
	// connection can be reused. The body is only measured, never logged
	// (design: Observability / Not logged - response bodies).
	maxErrorBodyBytes = 4096
)

// Sentinel errors let main choose an exit code without matching error strings.
// design: Failure policy.
var (
	// ErrUnexpectedStatus is returned for any non-2xx response. RemoteOK's feed
	// returns 200, configured by the source.
	ErrUnexpectedStatus = errors.New("unexpected HTTP status")

	// ErrRetryAfterTooLong is returned when a 429 asks us to wait longer than
	// RetryAfterCap. No retry is attempted and no sleep happens.
	ErrRetryAfterTooLong = errors.New("429 Retry-After exceeds cap")
)

// HTTPError carries the response code alongside ErrUnexpectedStatus.
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", ErrUnexpectedStatus, e.StatusCode)
}

// Unwrap makes errors.Is(err, ErrUnexpectedStatus) true for an HTTPError.
func (e *HTTPError) Unwrap() error { return ErrUnexpectedStatus }

// Fetch performs the single GET for a run and returns the raw response body, the
// HTTP status code, and an error.
//
// timeout is a whole-request deadline applied with context.WithTimeout, covering
// connection, redirects, and body read - not a per-connection idle timeout
// (design: Failure policy, "Request timeout").
//
// It does not parse JSON: step 6 is the caller's responsibility, and the body is
// returned verbatim so it can be persisted first (design: Run lifecycle step 5
// before step 6).
//
// One bounded retry is allowed, and only for HTTP 429 (design: Failure policy).
func Fetch(ctx context.Context, endpoint, userAgent string, timeout time.Duration) ([]byte, int, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          1,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: time.Second,
		},
	}
	defer client.CloseIdleConnections()

	body, status, retryAfter, err := fetchOnce(ctx, client, endpoint, userAgent)
	if err == nil {
		return body, status, nil
	}

	// Only 429 is retried, and only once.
	if status != http.StatusTooManyRequests {
		return nil, status, err
	}

	// An oversized Retry-After is a permanent failure, not a sleep.
	if errors.Is(err, ErrRetryAfterTooLong) {
		return nil, status, err
	}

	select {
	case <-ctx.Done():
		return nil, status, fmt.Errorf("waiting to retry after HTTP 429: %w", ctx.Err())
	case <-time.After(retryDelay(retryAfter)):
	}

	// Second occurrence of 429 is not retried again.
	body, status, _, err = fetchOnce(ctx, client, endpoint, userAgent)
	return body, status, err
}

// retryDelay converts a Retry-After header value to a sleep duration, applying
// the cap. A Retry-After may be either delta-seconds or an HTTP-date; only plain
// integers are honoured, and anything else falls back to the 30s default
// (design: Failure policy - "else 30s").
func retryDelay(header string) time.Duration {
	if header == "" {
		return defaultRetrySleep
	}
	secs, err := strconv.Atoi(header)
	if err != nil || secs < 0 {
		return defaultRetrySleep
	}
	d := time.Duration(secs) * time.Second
	if d > RetryAfterCap {
		return RetryAfterCap
	}
	return d
}

// fetchOnce performs exactly one request. It returns the HTTP status and the raw
// Retry-After header even on error, so the caller can classify the failure and
// decide whether a retry is allowed.
func fetchOnce(ctx context.Context, client *http.Client, endpoint, userAgent string) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("build request for %s: %w", endpoint, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// Covers DNS resolution, TLS handshake, and the context deadline.
		return nil, 0, "", err
	}
	defer func() {
		// Drain a little so the connection can be reused, then close.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		if secs, convErr := strconv.Atoi(retryAfter); convErr == nil && secs >= 0 {
			if time.Duration(secs)*time.Second > RetryAfterCap {
				return nil, resp.StatusCode, retryAfter, fmt.Errorf("%w: Retry-After=%ss (cap %s)",
					ErrRetryAfterTooLong, retryAfter, RetryAfterCap)
			}
		}
		return nil, resp.StatusCode, retryAfter, &HTTPError{StatusCode: resp.StatusCode}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, "", &HTTPError{StatusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// A body that fails mid-read is a truncated capture; it must not be
		// persisted as if it were complete.
		return nil, resp.StatusCode, "", fmt.Errorf("read response body: %w", err)
	}
	return body, resp.StatusCode, "", nil
}

// Parse decodes the response body into its raw elements. It requires a JSON
// array and does not inspect elements: skipping the legal notice happens during
// normalization, not here (design: Run lifecycle step 6).
func Parse(body []byte) ([]json.RawMessage, error) {
	var elements []json.RawMessage
	if err := json.Unmarshal(body, &elements); err != nil {
		return nil, fmt.Errorf("response is not a JSON array: %w", err)
	}
	if elements == nil {
		// "null" unmarshals to a nil slice without error; treat it as an empty
		// array so callers see a consistent zero-element result.
		return []json.RawMessage{}, nil
	}
	return elements, nil
}
