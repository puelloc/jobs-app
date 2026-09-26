// limiter_test.go: tests for the politeness layer.
//
// Time is injected throughout: the delay floor and the retry backoff are asserted without the suite
// actually waiting, so the tests stay fast and deterministic.
package httpfetch

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jobsapp/internal/careers"
)

// scriptedFetcher returns queued responses and records how many times it was called.
type scriptedFetcher struct {
	mu        sync.Mutex
	responses []careers.Response
	calls     int
}

func (f *scriptedFetcher) Fetch(_ context.Context, url string, _ int64) (careers.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.responses) == 0 {
		return careers.Response{FinalURL: url, Status: 200, ContentType: "text/html", Body: []byte("ok")}, nil
	}
	r := f.responses[0]
	if len(f.responses) > 1 {
		f.responses = f.responses[1:]
	}
	if r.FinalURL == "" {
		r.FinalURL = url
	}
	return r, nil
}

func (f *scriptedFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeClock makes delay and backoff instant while recording what was slept.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	slept   []time.Duration
	onSleep func(time.Duration)
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(0, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	// A fake that always returns nil would model sleeping *through* a cancellation, which is the
	// opposite of the real sleepContext and would let the retry loop ignore a cancelled run.
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	onSleep := c.onSleep
	c.mu.Unlock()
	if onSleep != nil {
		onSleep(d)
	}
	return ctx.Err()
}

func (c *fakeClock) sleptTotal() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total time.Duration
	for _, d := range c.slept {
		total += d
	}
	return total
}

func (c *fakeClock) sleptDurations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

func newTestLimiter(inner careers.Fetcher, clock *fakeClock, opts ...LimiterOption) *Limiter {
	opts = append(opts, WithClock(clock.Now, clock.Sleep))
	return NewLimiter(inner, opts...)
}

// --- retry policy ---------------------------------------------------------

func TestLimiterDoesNotRetryOn4xx(t *testing.T) {
	for _, status := range []int{403, 404, 410} {
		inner := &scriptedFetcher{responses: []careers.Response{{Status: status}}}
		l := newTestLimiter(inner, newFakeClock())

		resp, err := l.Fetch(context.Background(), "https://example.com/careers", 1<<20)
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if resp.Status != status {
			t.Errorf("status = %d, want %d", resp.Status, status)
		}
		if inner.callCount() != 1 {
			t.Errorf("status %d was fetched %d times, want 1: a 4xx answer will not change", status, inner.callCount())
		}
	}
}

func TestLimiterRetriesOn503WithBackoff(t *testing.T) {
	inner := &scriptedFetcher{responses: []careers.Response{{Status: 503}, {Status: 200}}}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock)

	resp, err := l.Fetch(context.Background(), "https://example.com/careers", 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want the retry to have succeeded", resp.Status)
	}
	if inner.callCount() != 2 {
		t.Errorf("called %d times, want 2", inner.callCount())
	}
	if clock.sleptTotal() != retryBackoff5xx {
		t.Errorf("slept %v, want %s", clock.sleptTotal(), retryBackoff5xx)
	}
}

func TestLimiterRetries5xxOnlyOnce(t *testing.T) {
	inner := &scriptedFetcher{responses: []careers.Response{{Status: 500}}}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock)

	resp, _ := l.Fetch(context.Background(), "https://example.com/careers", 1<<20)
	if resp.Status != 500 {
		t.Errorf("status = %d, want the last 500 returned", resp.Status)
	}
	// One initial attempt plus maxAttemptsServerError retries.
	if want := 1 + maxAttemptsServerError; inner.callCount() != want {
		t.Errorf("called %d times, want %d", inner.callCount(), want)
	}
}

func TestLimiterCapsRateLimitRetries(t *testing.T) {
	inner := &scriptedFetcher{responses: []careers.Response{{Status: 429}}}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock)

	resp, _ := l.Fetch(context.Background(), "https://example.com/careers", 1<<20)
	if resp.Status != 429 {
		t.Errorf("status = %d, want 429 returned after exhausting retries", resp.Status)
	}
	if want := 1 + maxAttemptsRateLimited; inner.callCount() != want {
		t.Errorf("called %d times, want %d", inner.callCount(), want)
	}
}

// Retry-After is honoured, because a host that states when to come back is telling us something the
// default backoff would ignore.
func TestLimiterHonorsRetryAfter(t *testing.T) {
	inner := &scriptedFetcher{responses: []careers.Response{
		{Status: 503, RetryAfter: "7"},
		{Status: 200},
	}}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock)

	if _, err := l.Fetch(context.Background(), "https://example.com/careers", 1<<20); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := clock.sleptTotal(); got != 7*time.Second {
		t.Errorf("slept %v, want the Retry-After value of 7s", got)
	}
}

// A host asking for an hour must not hold the run open. The value is capped.
func TestLimiterCapsAnOversizedRetryAfter(t *testing.T) {
	inner := &scriptedFetcher{responses: []careers.Response{
		{Status: 503, RetryAfter: "3600"},
		{Status: 200},
	}}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock)

	if _, err := l.Fetch(context.Background(), "https://example.com/careers", 1<<20); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := clock.sleptTotal(); got != maxRetryAfter {
		t.Errorf("slept %v, want it capped at %s", got, maxRetryAfter)
	}
}

// An HTTP-date form is also valid, and is what many hosts send.
func TestLimiterHonorsRetryAfterAsAnHTTPDate(t *testing.T) {
	clock := newFakeClock()
	when := clock.Now().Add(4 * time.Second).UTC().Format(http.TimeFormat)
	inner := &scriptedFetcher{responses: []careers.Response{
		{Status: 503, RetryAfter: when},
		{Status: 200},
	}}
	l := newTestLimiter(inner, clock)

	if _, err := l.Fetch(context.Background(), "https://example.com/careers", 1<<20); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The clock advances by whatever was slept, so a 4s delta is observed as 4s.
	if got := clock.sleptTotal(); got != 4*time.Second {
		t.Errorf("slept %v, want 4s", got)
	}
}

func TestLimiterFallsBackWhenRetryAfterIsUnparseable(t *testing.T) {
	inner := &scriptedFetcher{responses: []careers.Response{
		{Status: 503, RetryAfter: "soon"},
		{Status: 200},
	}}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock)

	if _, err := l.Fetch(context.Background(), "https://example.com/careers", 1<<20); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := clock.sleptTotal(); got != retryBackoff5xx {
		t.Errorf("slept %v, want the default %s", got, retryBackoff5xx)
	}
}

// A timeout is not retried: a host that could not answer in budget is slow, not unlucky, and
// retrying is how a batch spends its window on the few sites that cannot serve it.
func TestLimiterTimeoutIsNotRetried(t *testing.T) {
	inner := &scriptedFetcher{responses: []careers.Response{
		{TransportError: context.DeadlineExceeded, TimedOut: true},
		{Status: 200},
	}}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock)

	resp, err := l.Fetch(context.Background(), "https://example.com/careers", 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.TimedOut {
		t.Error("the timeout was lost")
	}
	if inner.callCount() != 1 {
		t.Errorf("called %d times, want 1: a timeout must not be retried", inner.callCount())
	}
	if clock.sleptTotal() != 0 {
		t.Errorf("slept %v, want nothing", clock.sleptTotal())
	}
}

// A refused connection is often transient, so it gets one retry - unlike a timeout.
func TestLimiterRetriesATransportFailureOnce(t *testing.T) {
	inner := &scriptedFetcher{responses: []careers.Response{
		{TransportError: errString("connection reset by peer")},
		{Status: 200},
	}}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock)

	resp, err := l.Fetch(context.Background(), "https://example.com/careers", 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want the retry to have succeeded", resp.Status)
	}
	if inner.callCount() != 2 {
		t.Errorf("called %d times, want 2", inner.callCount())
	}
}

// The retry loop must stop when the caller's context ends, not sleep through it.
func TestLimiterStopsRetryingWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := newFakeClock()
	clock.onSleep = func(time.Duration) { cancel() }

	inner := &scriptedFetcher{responses: []careers.Response{{Status: 503}}}
	l := newTestLimiter(inner, clock)

	if _, err := l.Fetch(ctx, "https://example.com/careers", 1<<20); err == nil {
		t.Fatal("Fetch kept retrying after the context was cancelled")
	}
}

// --- concurrency and delay ------------------------------------------------

// Two concurrent requests to the same host must not overlap. This is the politeness rule that
// replaced robots.txt obedience.
func TestLimiterSerializesPerHost(t *testing.T) {
	var inFlight, maxInFlight int32
	var mu sync.Mutex

	inner := fetcherFunc(func(ctx context.Context, url string, _ int64) (careers.Response, error) {
		n := atomic.AddInt32(&inFlight, 1)
		mu.Lock()
		if n > maxInFlight {
			maxInFlight = n
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return careers.Response{FinalURL: url, Status: 200}, nil
	})

	// Real clock here: the point is actual overlap, and a fake clock would not produce it. The
	// delay floor is disabled so the test measures concurrency alone.
	l := NewLimiter(inner, WithMinHostDelay(0), WithConcurrency(8))

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.Fetch(context.Background(), "https://example.com/careers", 1<<20)
		}()
	}
	wg.Wait()

	if maxInFlight != 1 {
		t.Errorf("max concurrent requests to one host = %d, want 1", maxInFlight)
	}
}

// Different hosts may proceed in parallel, bounded by the global cap.
func TestLimiterAllowsGlobalConcurrency(t *testing.T) {
	var inFlight, maxInFlight int32
	var mu sync.Mutex
	release := make(chan struct{})

	inner := fetcherFunc(func(ctx context.Context, url string, _ int64) (careers.Response, error) {
		n := atomic.AddInt32(&inFlight, 1)
		mu.Lock()
		if n > maxInFlight {
			maxInFlight = n
		}
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		}
		atomic.AddInt32(&inFlight, -1)
		return careers.Response{FinalURL: url, Status: 200}, nil
	})

	l := NewLimiter(inner, WithMinHostDelay(0), WithConcurrency(3))

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = l.Fetch(context.Background(), hostURL(i), 1<<20)
		}(i)
	}

	// Wait until all three are in flight, which can only happen if the global cap allows it.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		reached := maxInFlight
		mu.Unlock()
		if reached >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d requests ran concurrently across distinct hosts, want 3", reached)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(release)
	wg.Wait()
}

func hostURL(i int) string {
	return "https://host" + string(rune('a'+i)) + ".example.com/careers"
}

// The per-host floor is applied between successive requests to one host, and not to the first.
func TestLimiterAppliesPerHostDelay(t *testing.T) {
	inner := &scriptedFetcher{}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock, WithMinHostDelay(500*time.Millisecond))

	// The first request has no predecessor and must not wait.
	if _, err := l.Fetch(context.Background(), "https://example.com/a", 1<<20); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if clock.sleptTotal() != 0 {
		t.Errorf("the first request to a host slept %v, want nothing", clock.sleptTotal())
	}

	// The second is held for the floor.
	if _, err := l.Fetch(context.Background(), "https://example.com/b", 1<<20); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if got := clock.sleptTotal(); got != 500*time.Millisecond {
		t.Errorf("the second request slept %v, want the 500ms floor", got)
	}
}

// A different host is not delayed by another host's traffic.
func TestLimiterDelayIsPerHostNotGlobal(t *testing.T) {
	inner := &scriptedFetcher{}
	clock := newFakeClock()
	l := newTestLimiter(inner, clock, WithMinHostDelay(500*time.Millisecond))

	if _, err := l.Fetch(context.Background(), "https://a.example.com/x", 1<<20); err != nil {
		t.Fatalf("Fetch a: %v", err)
	}
	if _, err := l.Fetch(context.Background(), "https://b.example.com/x", 1<<20); err != nil {
		t.Fatalf("Fetch b: %v", err)
	}
	if clock.sleptTotal() != 0 {
		t.Errorf("a different host waited %v, want nothing", clock.sleptTotal())
	}
}

// A cancelled context returns an error rather than a fabricated response, so a shutdown does not
// write attempts for requests that never happened.
func TestLimiterReportsCancellationRatherThanAResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	inner := &scriptedFetcher{}
	l := newTestLimiter(inner, newFakeClock())

	if _, err := l.Fetch(ctx, "https://example.com/careers", 1<<20); err == nil {
		t.Fatal("Fetch returned no error for a cancelled context")
	}
	if inner.callCount() != 0 {
		t.Errorf("the inner fetcher was called %d times after cancellation, want 0", inner.callCount())
	}
}

func TestLimiterSatisfiesTheCareersSeam(t *testing.T) {
	var _ careers.Fetcher = NewLimiter(&scriptedFetcher{})
}

// fetcherFunc adapts a function to the seam.
type fetcherFunc func(context.Context, string, int64) (careers.Response, error)

func (f fetcherFunc) Fetch(ctx context.Context, url string, maxBodyBytes int64) (careers.Response, error) {
	return f(ctx, url, maxBodyBytes)
}

type errString string

func (e errString) Error() string { return strings.TrimSpace(string(e)) }

// The cancellation check must be deterministic, not probabilistic.
//
// With an already-cancelled context and a free slot, both select cases are ready and Go picks at
// random, so roughly half of the acquisitions succeeded - a request escaping a cancelled run. One
// iteration would pass about half the time, which is why this loops: the bug is only visible when
// the same case is exercised repeatedly.
func TestCancelledContextNeverAcquiresASlot(t *testing.T) {
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		inner := &scriptedFetcher{}
		l := newTestLimiter(inner, newFakeClock())

		if _, err := l.Fetch(ctx, "https://example.com/careers", 1<<20); err == nil {
			t.Fatalf("iteration %d: Fetch proceeded with a cancelled context", i)
		}
		if n := inner.callCount(); n != 0 {
			t.Fatalf("iteration %d: the inner fetcher ran %d times after cancellation", i, n)
		}
	}
}
