// httpfetch_test.go: tests for the real HTTP fetcher.
//
// Every test runs against an httptest.Server on loopback. Nothing here reaches the public internet,
// which is what lets the fetcher's policy - redirects, TLS, proxy, bounds, timeout - be pinned.
package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jobsapp/internal/careers"
)

const testAgent = "jobs-app-test/0.1 (+https://example.test)"

func newTestFetcher(t *testing.T, opts ...Option) *Fetcher {
	t.Helper()
	f, err := New(testAgent, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

// A 4xx is an answer, not a Go error. The gate classifies it; collapsing it into an error would make
// a blocked site indistinguishable from an unreachable one.
func TestFetcherReturnsResponseFor4xxNotError(t *testing.T) {
	for _, status := range []int{403, 404, 429} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("denied"))
		}))
		f := newTestFetcher(t)
		resp, err := f.Fetch(context.Background(), srv.URL, 1<<20)
		srv.Close()

		if err != nil {
			t.Errorf("status %d returned a Go error (%v), want (Response, nil)", status, err)
			continue
		}
		if resp.Status != status {
			t.Errorf("status = %d, want %d", resp.Status, status)
		}
		if resp.TransportError != nil {
			t.Errorf("status %d set TransportError %v, want nil", status, resp.TransportError)
		}
	}
}

func TestFetcherReturns2xxBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head><title>Acme</title></head></html>"))
	}))
	defer srv.Close()

	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "<title>Acme</title>") {
		t.Errorf("body = %q", resp.Body)
	}
	if !strings.Contains(resp.ContentType, "text/html") {
		t.Errorf("content type = %q, want the header surfaced", resp.ContentType)
	}
	if resp.Truncated {
		t.Error("Truncated = true for a small body")
	}
}

// A connection refused produces an error-valued Response rather than a Go error, so the gate can
// record it as an unknown rather than a rejection.
func TestFetcherReturnsErrorForTransportFailure(t *testing.T) {
	// Bind a port and close it, so the address is well-formed and refuses connections.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	resp, err := newTestFetcher(t).Fetch(context.Background(), url, 1<<20)
	if err != nil {
		t.Fatalf("Fetch returned a Go error (%v), want the failure inside Response.TransportError", err)
	}
	if resp.TransportError == nil {
		t.Fatal("TransportError is nil for an unreachable host")
	}
	if resp.TimedOut {
		t.Error("TimedOut = true for a refused connection")
	}
	if resp.Status != 0 {
		t.Errorf("status = %d, want 0 when no response arrived", resp.Status)
	}
}

func TestFetcherReturnsErrorForTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(200)
	}))
	defer func() { close(release); srv.Close() }()

	f := newTestFetcher(t, WithTimeout(50*time.Millisecond))
	resp, err := f.Fetch(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TransportError == nil {
		t.Fatal("TransportError is nil for a request that timed out")
	}
	if !resp.TimedOut {
		t.Errorf("TimedOut = false for a deadline error %v", resp.TransportError)
	}
}

// A timeout must be distinguishable from a refusal at the gate, because the retry policies differ.
func TestFetcherDistinguishesTimeoutFromTransportError(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	f := newTestFetcher(t, WithTimeout(50*time.Millisecond))

	timedOut, _ := f.Fetch(context.Background(), slow.URL, 1<<20)
	refused, _ := f.Fetch(context.Background(), closedURL, 1<<20)

	if !timedOut.TimedOut {
		t.Errorf("a slow host did not report TimedOut: %v", timedOut.TransportError)
	}
	if refused.TimedOut {
		t.Errorf("a refused connection reported TimedOut: %v", refused.TransportError)
	}

	// And the gate maps them to different reasons.
	timeoutVerdict := careers.Validate(
		careers.Candidate{URL: slow.URL, Kind: careers.KindWebsite}, timedOut)
	refusedVerdict := careers.Validate(
		careers.Candidate{URL: closedURL, Kind: careers.KindWebsite}, refused)
	if timeoutVerdict.Reason == refusedVerdict.Reason {
		t.Fatalf("both produced reason %q; the retry policy cannot tell them apart", timeoutVerdict.Reason)
	}
}

func TestFetcherFollowsRedirectsUpToLimit(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := 0
		fmt.Sscanf(r.URL.Query().Get("hop"), "%d", &n)
		if n >= 3 {
			_, _ = w.Write([]byte("arrived"))
			return
		}
		http.Redirect(w, r, fmt.Sprintf("%s/?hop=%d", srv.URL, n+1), http.StatusFound)
	}))

	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL+"/?hop=0", 1<<20)
	srv.Close()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want the final 200", resp.Status)
	}
	if string(resp.Body) != "arrived" {
		t.Errorf("body = %q, want the final document", resp.Body)
	}
}

// The final URL is the document actually read. This is what identifies an applicant-tracking vendor
// when a branded careers page redirects to it.
func TestFetcherSurfacesFinalURLDistinctFromRequestURL(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("target"))
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/careers", http.StatusMovedPermanently)
	}))
	defer srv.Close()

	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.FinalURL != target.URL+"/careers" {
		t.Errorf("FinalURL = %q, want the redirect target", resp.FinalURL)
	}
	if !strings.HasPrefix(resp.FinalURL, target.URL) {
		t.Errorf("FinalURL = %q, want it on the target host", resp.FinalURL)
	}
}

func TestFetcherErrorsOnRedirectLoop(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	}))
	defer srv.Close()

	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch returned a Go error (%v), want the failure inside Response", err)
	}
	if resp.TransportError == nil {
		t.Fatal("TransportError is nil for a redirect loop")
	}
	if !errors.Is(resp.TransportError, ErrTooManyRedirects) {
		t.Errorf("TransportError = %v, want it to wrap ErrTooManyRedirects", resp.TransportError)
	}
}

func TestFetcherSetsTruncatedWhenBodyExceedsLimit(t *testing.T) {
	payload := strings.Repeat("x", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1024)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(resp.Body) != 1024 {
		t.Errorf("body length = %d, want the bound 1024", len(resp.Body))
	}
	if !resp.Truncated {
		t.Error("Truncated = false for a body that hit the bound")
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want the response kept alongside the truncation", resp.Status)
	}
}

func TestFetcherDoesNotSetTruncatedForABodyWithinTheLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("small"))
	}))
	defer srv.Close()

	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Truncated {
		t.Error("Truncated = true for a body within the bound")
	}
	if string(resp.Body) != "small" {
		t.Errorf("body = %q", resp.Body)
	}
}

// A body of exactly the bound is complete, not truncated. Off-by-one here would mark every
// right-sized document as cut.
func TestFetcherDoesNotTruncateABodyExactlyAtTheBound(t *testing.T) {
	payload := strings.Repeat("y", 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1024)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Truncated {
		t.Error("Truncated = true for a body exactly at the bound")
	}
	if len(resp.Body) != 1024 {
		t.Errorf("body length = %d, want 1024", len(resp.Body))
	}
}

func TestFetcherUsesConfiguredUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if _, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1<<20); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != testAgent {
		t.Errorf("User-Agent = %q, want %q", got, testAgent)
	}
}

// An empty agent is a startup error, not a silent fallback to something browser-like.
func TestFetcherRejectsAnEmptyUserAgent(t *testing.T) {
	if _, err := New(""); !errors.Is(err, ErrEmptyUserAgent) {
		t.Errorf("New(\"\") error = %v, want ErrEmptyUserAgent", err)
	}
}

// HTTP_PROXY is commonly set on a developer machine and on some NAS setups. Inheriting it would
// route the crawl somewhere unexpected, and the symptom would look like a network fault.
func TestFetcherDoesNotInheritProxyFromEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TransportError != nil {
		t.Fatalf("request failed with a proxy in the environment: %v", resp.TransportError)
	}
	if !reached {
		t.Error("the origin was not reached, which means the request went via the proxy")
	}
}

// TLS is verified. The httptest TLS server uses a certificate the default pool does not trust, so a
// successful fetch requires injecting that server's own client - which is exactly the point: there
// is no InsecureSkipVerify switch to reach for.
func TestFetcherVerifiesTLSByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secure"))
	}))
	defer srv.Close()

	// Default transport: the self-signed certificate is rejected.
	resp, err := newTestFetcher(t).Fetch(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TransportError == nil {
		t.Fatal("an untrusted certificate was accepted; TLS verification is not on")
	}

	// With the server's own trusted client injected, the same request succeeds.
	f := newTestFetcher(t, WithHTTPClient(srv.Client()))
	trusted, err := f.Fetch(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch with the test client: %v", err)
	}
	if trusted.Status != 200 {
		t.Errorf("status = %d, want 200 once the certificate is trusted", trusted.Status)
	}
	if string(trusted.Body) != "secure" {
		t.Errorf("body = %q", trusted.Body)
	}
}

// A context deadline shorter than the fetcher timeout wins, so a caller can bound a single request
// without reconstructing the fetcher.
func TestFetcherHonoursACallerDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	resp, err := newTestFetcher(t).Fetch(ctx, srv.URL, 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TransportError == nil {
		t.Fatal("the caller's deadline was ignored")
	}
	if !resp.TimedOut {
		t.Error("TimedOut = false for a caller deadline")
	}
}

// The fetcher satisfies the seam it was written for.
func TestFetcherSatisfiesTheCareersSeam(t *testing.T) {
	var _ careers.Fetcher = newTestFetcher(t)
}
