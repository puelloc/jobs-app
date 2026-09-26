// limiter.go: the politeness layer.
//
// Rate limiting is the politeness mechanism for this crawler, not robots.txt. A weekly batch hitting
// roughly 1,500 corporate sites keeps a low profile by being slow and serial per host rather than by
// reading and obeying each host's rules file: under that decision robots.txt is a discovery signal
// (its Sitemap: line occasionally names the careers host) and never a fetch gate.
//
// See docs/sp1500-plan.md, section 2.2, for why the two were separated.
package httpfetch

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"jobsapp/internal/careers"
)

// Retry policy, per the plan:
//
//	4xx            no retry - the answer will not change
//	5xx            one retry after a short backoff
//	429, 503       up to three retries, honouring Retry-After
//	timeout        no retry - see below
//	transport      one retry - a refused connection is often transient
//
// A timeout is deliberately not retried. A host that could not answer within the budget is more
// likely to be slow than unlucky, and retrying it is how a batch run spends its window on the
// handful of sites that cannot serve us.
const (
	maxAttemptsRateLimited = 3
	maxAttemptsServerError = 1
	maxAttemptsTransport   = 1
	retryBackoff5xx        = 2 * time.Second
	retryBackoffDefault    = 1 * time.Second
	maxRetryAfter          = 30 * time.Second
)

// Defaults for the limiter. Per-host concurrency of one with a half-second floor is the whole
// politeness story; the global cap keeps total in-flight requests modest without making the run
// take days.
const (
	DefaultConcurrency  = 4
	DefaultMinHostDelay = 500 * time.Millisecond
)

// Limiter wraps a Fetcher with concurrency bounds, a per-host delay, and a retry policy.
type Limiter struct {
	inner careers.Fetcher

	// global bounds total in-flight requests across all hosts.
	global chan struct{}
	// perHost serialises requests to one host. Capacity one is the point: two concurrent requests
	// to the same origin is what a site notices.
	perHost *keyedSemaphore
	// lastRequest records the time of the previous request per host, for the delay floor.
	lastRequest *keyedTimes

	minDelay time.Duration
	// now and sleep are injectable so the retry and delay behaviour can be tested without the
	// suite actually waiting.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// LimiterOption adjusts a Limiter at construction.
type LimiterOption func(*Limiter)

// WithConcurrency sets the global in-flight cap.
func WithConcurrency(n int) LimiterOption {
	return func(l *Limiter) {
		if n > 0 {
			l.global = make(chan struct{}, n)
		}
	}
}

// WithMinHostDelay sets the floor between two requests to the same host.
func WithMinHostDelay(d time.Duration) LimiterOption {
	return func(l *Limiter) { l.minDelay = d }
}

// WithClock replaces the time source. Tests use it to make delay and backoff assertions instant.
func WithClock(now func() time.Time, sleep func(context.Context, time.Duration) error) LimiterOption {
	return func(l *Limiter) {
		l.now = now
		l.sleep = sleep
	}
}

// NewLimiter wraps inner.
func NewLimiter(inner careers.Fetcher, opts ...LimiterOption) *Limiter {
	l := &Limiter{
		inner:       inner,
		global:      make(chan struct{}, DefaultConcurrency),
		perHost:     newKeyedSemaphore(),
		lastRequest: newKeyedTimes(),
		minDelay:    DefaultMinHostDelay,
		now:         time.Now,
		sleep:       sleepContext,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Fetch applies the retry policy around the wrapped fetcher, with each attempt passing through the
// concurrency and delay bounds.
func (l *Limiter) Fetch(ctx context.Context, rawURL string, maxBodyBytes int64) (careers.Response, error) {
	host := hostOf(rawURL)

	attempt := 0
	for {
		resp, err := l.boundedFetch(ctx, host, rawURL, maxBodyBytes)
		if err != nil {
			return resp, err
		}

		retryable, after := l.retryDecision(resp, attempt)
		if !retryable {
			return resp, nil
		}
		attempt++

		if after <= 0 {
			after = retryBackoffDefault
		}
		if err := l.sleep(ctx, after); err != nil {
			// The caller's context ended while waiting. The host may still be fine, but there is
			// no run left to use the answer.
			return resp, err
		}
	}
}

// retryDecision reports whether another attempt is warranted, and how long to wait first.
//
// attempt is the number of retries already performed, so attempt 0 is the first response.
func (l *Limiter) retryDecision(resp careers.Response, attempt int) (bool, time.Duration) {
	if resp.TransportError != nil {
		// A timeout is not retried; a refusal may be transient, but only once.
		if resp.TimedOut || isTimeout(resp.TransportError) {
			return false, 0
		}
		if attempt < maxAttemptsTransport {
			return true, retryBackoffDefault
		}
		return false, 0
	}

	switch {
	case resp.Status == http.StatusTooManyRequests || resp.Status == http.StatusServiceUnavailable:
		if attempt >= maxAttemptsRateLimited {
			return false, 0
		}
		return true, l.retryAfter(resp)
	case resp.Status >= 500:
		if attempt >= maxAttemptsServerError {
			return false, 0
		}
		return true, retryBackoff5xx
	default:
		// 2xx and every other 4xx are final answers.
		return false, 0
	}
}

// retryAfter reads the Retry-After header, capped so a host cannot hold the run open for hours.
//
// Both forms the header allows are handled: delta-seconds and an HTTP date. An unparseable or
// absent value falls back to the default backoff.
func (l *Limiter) retryAfter(resp careers.Response) time.Duration {
	raw := resp.RetryAfter
	if raw == "" {
		return retryBackoff5xx
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return capDelay(time.Duration(secs) * time.Second)
	}
	if when, err := http.ParseTime(raw); err == nil {
		return capDelay(when.Sub(l.now()))
	}
	return retryBackoff5xx
}

func capDelay(d time.Duration) time.Duration {
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	if d < 0 {
		return 0
	}
	return d
}

// boundedFetch performs one attempt through the per-host and global bounds.
func (l *Limiter) boundedFetch(ctx context.Context, host, rawURL string, maxBodyBytes int64) (careers.Response, error) {
	release, err := l.acquire(ctx, host)
	if err != nil {
		return careers.Response{TransportError: err}, nil
	}
	defer release()

	if err := l.waitForHostDelay(ctx, host); err != nil {
		return careers.Response{TransportError: err}, nil
	}
	defer func() { l.lastRequest.set(host, l.now()) }()

	return l.inner.Fetch(ctx, rawURL, maxBodyBytes)
}

// acquire takes the global slot, then the per-host slot. Global first so a host waiting on its own
// turn does not hold a global slot while it waits.
func (l *Limiter) acquire(ctx context.Context, host string) (func(), error) {
	if err := acquireSlot(ctx, l.global); err != nil {
		return nil, err
	}
	if err := l.perHost.acquire(ctx, host); err != nil {
		releaseSlot(l.global)
		return nil, err
	}
	return func() {
		l.perHost.release(host)
		releaseSlot(l.global)
	}, nil
}

// waitForHostDelay sleeps until the per-host floor has elapsed since the previous request.
func (l *Limiter) waitForHostDelay(ctx context.Context, host string) error {
	last, ok := l.lastRequest.get(host)
	if !ok {
		return nil
	}
	if wait := l.minDelay - l.now().Sub(last); wait > 0 {
		return l.sleep(ctx, wait)
	}
	return nil
}

// acquireSlot takes a global slot. The context is checked first for the same reason as the per-host
// semaphore: an already-cancelled context and a free slot are both ready, and select would choose
// between them at random.
func acquireSlot(ctx context.Context, sem chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSlot(sem chan struct{}) { <-sem }

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func hostOf(rawURL string) string {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return rawURL
	}
	return req.URL.Host
}

// retryAfterFrom returns the Retry-After header carried on a response, if the fetcher surfaced one.
//
// The careers seam keeps a ContentType string rather than a header map, so the header is passed
// through a package-level side channel set by a fetcher that reads it. When it is absent the
// backoff is the default, which is a correct if less precise behaviour.
func retryAfterFrom(resp careers.Response) string {
	if resp.RetryAfter != "" {
		return resp.RetryAfter
	}
	return ""
}
