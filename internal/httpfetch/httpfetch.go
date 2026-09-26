// Package httpfetch implements the careers.Fetcher seam against the standard library.
//
// It exists so internal/careers can be tested without a socket, and so the network policy lives in
// one place: redirects, TLS, proxies, body bounds, timeouts and the User-Agent. Every rule here has
// a test against an httptest.Server; nothing in this package reaches the public internet in a test.
//
// design: docs/sp1500-plan.md, section 3.
package httpfetch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"jobsapp/internal/careers"
)

// defaultTimeout bounds a single request when the caller's context has no earlier deadline. The
// ATS JSON endpoints need longer, so callers set their own.
const defaultTimeout = 15 * time.Second

// maxRedirects is the hop limit. Exceeding it is an error rather than a stored 3xx: the final
// document was never read, so there is nothing to validate.
const maxRedirects = 5

// ErrTooManyRedirects is returned when a redirect chain exceeds maxRedirects.
var ErrTooManyRedirects = errors.New("httpfetch: too many redirects")

// ErrEmptyUserAgent is returned by New when no User-Agent is configured.
//
// There is no default that impersonates a browser, and there is no silent fallback: an empty agent
// is a configuration mistake, and discovering it mid-run as a wave of 403s would be far worse than
// refusing to start.
var ErrEmptyUserAgent = errors.New("httpfetch: User-Agent must not be empty")

// Fetcher is a careers.Fetcher backed by net/http.
type Fetcher struct {
	client    *http.Client
	userAgent string
	timeout   time.Duration
}

// Option adjusts a Fetcher at construction.
type Option func(*Fetcher)

// WithTimeout overrides the per-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(f *Fetcher) { f.timeout = d }
}

// WithHTTPClient replaces the underlying client. Tests use it to inject an httptest TLS client's
// transport; production code does not need it.
func WithHTTPClient(c *http.Client) Option {
	return func(f *Fetcher) { f.client = c }
}

// New returns a Fetcher.
//
// The client is built rather than inherited from http.DefaultClient so that two defaults are
// explicit:
//
//   - Proxy is nil. The NAS environment may have HTTP_PROXY set, and silently routing a 1,500-site
//     crawler through it would be a surprising failure that looks like a network problem.
//   - TLS verification is on. There is no InsecureSkipVerify switch, deliberately: a crawler that
//     can be told to ignore certificates will eventually be told to, and the resulting data is
//     indistinguishable from data fetched from the right host.
func New(userAgent string, opts ...Option) (*Fetcher, error) {
	if userAgent == "" {
		return nil, ErrEmptyUserAgent
	}
	f := &Fetcher{userAgent: userAgent, timeout: defaultTimeout}
	for _, opt := range opts {
		opt(f)
	}
	if f.client == nil {
		f.client = &http.Client{
			Transport: &http.Transport{
				Proxy:               nil,
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     30 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
			CheckRedirect: checkRedirect,
		}
	}
	return f, nil
}

// checkRedirect stops a chain that runs longer than maxRedirects.
//
// Go's default is ten hops, which is both more than intended and unscoped. The hops seen so far are
// the length of req via's request list.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return ErrTooManyRedirects
	}
	return nil
}

// Fetch performs one request and returns a bounded body.
//
// A non-2xx response is returned as a Response with a nil error: 403, 429 and 404 are answers the
// validation gate classifies, and collapsing them into a Go error would make a blocked site
// indistinguishable from a dead one. A non-nil error means no complete response was received.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, maxBodyBytes int64) (careers.Response, error) {
	timeout := f.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return careers.Response{}, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")

	resp, err := f.client.Do(req)
	if err != nil {
		// No complete response was received. The body-less Response carries the cause so the gate
		// can classify it, and TimedOut is set here so a caller need not inspect the error chain.
		return careers.Response{
			FinalURL:       rawURL,
			TransportError: err,
			TimedOut:       isTimeout(err),
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	limited := io.LimitReader(resp.Body, maxBodyBytes+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return careers.Response{
			FinalURL:       resp.Request.URL.String(),
			Status:         resp.StatusCode,
			TransportError: readErr,
			TimedOut:       isTimeout(readErr),
		}, nil
	}

	truncated := false
	if maxBodyBytes >= 0 && int64(len(body)) > maxBodyBytes {
		body = body[:maxBodyBytes]
		truncated = true
	}

	return careers.Response{
		FinalURL:    resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		Truncated:   truncated,
	}, nil
}

// isTimeout reports whether err is a deadline, from either the context or the socket.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return errors.Is(err, syscall.ETIMEDOUT)
}
