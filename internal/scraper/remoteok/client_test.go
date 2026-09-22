// client_test.go: offline unit tests for Parse, Fetch, and the retry policy.
//
// Every network test runs against a local httptest server. Nothing here reaches
// the real RemoteOK endpoint.
package remoteok

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The response array is heterogeneous: element 0 is the legal notice and carries
// no id, elements 1 and 2 are jobs. This shape matches the real capture in
// data/raw/, but the test does not depend on that file.
const mixedArrayBody = `[
  {"legal": "RemoteOK API terms and attribution notice"},
  {"id": "1001", "position": "Go Engineer"},
  {"id": "1002", "position": "Backend Engineer"}
]`

func TestParseReturnsEveryElement(t *testing.T) {
	elements, err := Parse([]byte(mixedArrayBody))
	if err != nil {
		t.Fatalf("Parse returned error for a valid array: %v", err)
	}
	if len(elements) != 3 {
		t.Fatalf("Parse returned %d elements, want 3", len(elements))
	}
	// Parse must not skip the notice: skipping is normalization's job
	// (design: Run lifecycle step 6).
	if got := strings.TrimSpace(string(elements[0])); !strings.Contains(got, "legal") {
		t.Errorf("element 0 = %s, want the untouched legal notice", got)
	}
	if got := strings.TrimSpace(string(elements[1])); !strings.Contains(got, "1001") {
		t.Errorf("element 1 = %s, want the first job verbatim", got)
	}
}

func TestParseRejectsNonArray(t *testing.T) {
	if _, err := Parse([]byte(`{"id": "1001"}`)); err == nil {
		t.Fatal("Parse accepted a JSON object, want an error requiring an array")
	}
	if _, err := Parse([]byte(`not json at all`)); err == nil {
		t.Fatal("Parse accepted invalid JSON, want an error")
	}
	if _, err := Parse([]byte(`<!doctype html><html></html>`)); err == nil {
		t.Fatal("Parse accepted HTML, want an error")
	}
}

func TestParseEmptyArray(t *testing.T) {
	elements, err := Parse([]byte(`[]`))
	if err != nil {
		t.Fatalf("Parse returned error for an empty array: %v", err)
	}
	if len(elements) != 0 {
		t.Fatalf("Parse returned %d elements, want 0", len(elements))
	}
}

func TestParseNullIsTreatedAsEmpty(t *testing.T) {
	elements, err := Parse([]byte(`null`))
	if err != nil {
		t.Fatalf("Parse returned error for null: %v", err)
	}
	if len(elements) != 0 {
		t.Fatalf("Parse returned %d elements for null, want 0", len(elements))
	}
}

// mixed-type arrays: the notice element is not a job, but it is still an element.
func TestParseKeepsMixedElementTypes(t *testing.T) {
	elements, err := Parse([]byte(`[{"legal":"x"}, "bare string", 42]`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(elements) != 3 {
		t.Fatalf("Parse returned %d elements, want 3", len(elements))
	}
}

// --- Fetch ---------------------------------------------------------------

func TestFetchSuccessReturnsBodyStatusAndHeaders(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mixedArrayBody))
	}))
	defer srv.Close()

	body, status, err := Fetch(context.Background(), srv.URL, "test-agent/1.0", 5*time.Second)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if string(body) != mixedArrayBody {
		t.Errorf("body was modified in transit: got %d bytes, want %d", len(body), len(mixedArrayBody))
	}
	if gotUA != "test-agent/1.0" {
		t.Errorf("User-Agent = %q, want the configured value", gotUA)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
}

func TestFetchDoesNotParseOrModifyBody(t *testing.T) {
	// A body that is not JSON must still be returned verbatim: persisting the raw
	// payload before parsing is what makes a parse failure recoverable.
	const html = "<!doctype html><html>not json</html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	body, status, err := Fetch(context.Background(), srv.URL, "ua", 5*time.Second)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if status != http.StatusOK || string(body) != html {
		t.Fatalf("got status=%d body=%q, want 200 and the HTML body verbatim", status, body)
	}
}

func TestFetchStatusErrorsAreNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(code)
			}))
			defer srv.Close()

			_, status, err := Fetch(context.Background(), srv.URL, "ua", 5*time.Second)
			if err == nil {
				t.Fatal("Fetch returned no error for a non-2xx status")
			}
			if !errors.Is(err, ErrUnexpectedStatus) {
				t.Errorf("err = %v, want it to wrap ErrUnexpectedStatus", err)
			}
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("err = %v, want an *HTTPError", err)
			}
			if httpErr.StatusCode != code {
				t.Errorf("HTTPError.StatusCode = %d, want %d", httpErr.StatusCode, code)
			}
			if status != code {
				t.Errorf("returned status = %d, want %d", status, code)
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Errorf("server saw %d requests, want exactly 1 (no retry for %d)", got, code)
			}
		})
	}
}

func TestHTTPErrorWrapsSentinel(t *testing.T) {
	err := &HTTPError{StatusCode: 403}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Error("errors.Is(*HTTPError, ErrUnexpectedStatus) = false, want true")
	}
	if msg := err.Error(); !strings.Contains(msg, "403") {
		t.Errorf("Error() = %q, want it to mention the status code", msg)
	}
}

func TestFetchRetriesOnceOn429ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "1") // 1s keeps the test fast
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	body, status, err := Fetch(context.Background(), srv.URL, "ua", 10*time.Second)
	if err != nil {
		t.Fatalf("Fetch returned error after a retryable 429: %v", err)
	}
	if status != http.StatusOK || string(body) != "[]" {
		t.Fatalf("got status=%d body=%q, want 200 and []", status, body)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server saw %d requests, want 2 (one retry)", got)
	}
}

func TestFetchGivesUpAfterSecond429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, status, err := Fetch(context.Background(), srv.URL, "ua", 10*time.Second)
	if err == nil {
		t.Fatal("Fetch returned no error when every attempt was 429")
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", status)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server saw %d requests, want exactly 2 (one retry, then give up)", got)
	}
}

func TestFetchDoesNotSleepOnOversizedRetryAfter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "86400") // a day: must be refused, not slept on
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	start := time.Now()
	_, status, err := Fetch(context.Background(), srv.URL, "ua", 10*time.Second)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrRetryAfterTooLong) {
		t.Fatalf("err = %v, want ErrRetryAfterTooLong", err)
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", status)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d requests, want 1 (oversized Retry-After is permanent)", got)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Fetch took %s, want it to fail immediately without sleeping", elapsed)
	}
}

func TestFetchRetriesOn429WithNonNumericRetryAfter(t *testing.T) {
	// A non-integer Retry-After (e.g. an HTTP-date) falls back to the 30s default,
	// so cap the wait by making the context expire during the sleep.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, status, err := Fetch(ctx, srv.URL, "ua", 10*time.Second)
	if err == nil {
		t.Fatal("Fetch returned no error")
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", status)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d requests, want 1 (the wait was cut short by ctx)", got)
	}
}

func TestFetchRespectsContextDeadline(t *testing.T) {
	release := make(chan struct{})
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release // block until the test finishes, simulating a slow origin
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	start := time.Now()
	_, _, err := Fetch(context.Background(), srv.URL, "ua", 250*time.Millisecond)
	if err == nil {
		t.Fatal("Fetch returned no error for a request that exceeded its timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Fetch took %s, want it to abort near the 250ms deadline", elapsed)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d requests, want 1 (a timeout is not retried)", got)
	}
}

func TestFetchConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // now nothing is listening

	_, _, err := Fetch(context.Background(), url, "ua", 2*time.Second)
	if err == nil {
		t.Fatal("Fetch returned no error against a closed server")
	}
	if errors.Is(err, ErrUnexpectedStatus) {
		t.Errorf("err = %v, want a transport error rather than an HTTP status error", err)
	}
}

func TestFetchRejectsUnparseableURL(t *testing.T) {
	if _, _, err := Fetch(context.Background(), "://not a url", "ua", time.Second); err == nil {
		t.Fatal("Fetch accepted a malformed URL")
	}
}

func TestFetchUsesDefaultTimeoutWhenNonPositive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	// A non-positive timeout must fall back to DefaultTimeout rather than
	// cancelling the request instantly.
	if _, status, err := Fetch(context.Background(), srv.URL, "ua", 0); err != nil || status != http.StatusOK {
		t.Fatalf("Fetch with timeout=0: status=%d err=%v, want 200 and no error", status, err)
	}
}

// --- retry policy --------------------------------------------------------

func TestRetryDelay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"absent falls back to default", "", defaultRetrySleep},
		{"plain seconds", "15", 15 * time.Second},
		{"zero", "0", 0},
		{"exactly the cap", "60", RetryAfterCap},
		{"above the cap clamps", "61", RetryAfterCap},
		{"far above the cap clamps", "86400", RetryAfterCap},
		{"non-numeric falls back", "abc", defaultRetrySleep},
		{"http-date falls back", "Wed, 21 Oct 2026 07:28:00 GMT", defaultRetrySleep},
		{"negative falls back", "-5", defaultRetrySleep},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryDelay(tc.header); got != tc.want {
				t.Errorf("retryDelay(%q) = %s, want %s", tc.header, got, tc.want)
			}
		})
	}
}

func TestRetryAfterCapIsSixtySeconds(t *testing.T) {
	if RetryAfterCap != 60*time.Second {
		t.Errorf("RetryAfterCap = %s, want 1m0s", RetryAfterCap)
	}
}

func TestParseThenJobDecodeRoundTrip(t *testing.T) {
	// Parse output must be decodable into Job without losing the id.
	elements, err := Parse([]byte(mixedArrayBody))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var job Job
	if err := json.Unmarshal(elements[1], &job); err != nil {
		t.Fatalf("decode element 1 into Job: %v", err)
	}
	id, err := job.ID()
	if err != nil {
		t.Fatalf("ID(): %v", err)
	}
	if id != "1001" {
		t.Errorf("ID() = %q, want 1001", id)
	}
	if job.Title != "Go Engineer" {
		t.Errorf("Title = %q, want the position value", job.Title)
	}
}
