// recording.go: a Fetcher decorator that records what it was asked for.
//
// It lives in the seam package rather than in a _test.go file because more than one tier wraps its
// fetcher with it: tier 1 asserts that every batched call carries a byte bound, and the command
// wiring asserts the per-host delay is not bypassed. A test-only copy in each package would drift
// from the others, which is the diverging-fake problem in slow motion.
package careers

import (
	"context"
	"sync"
)

// RecordedCall is one fetch as the decorator saw it.
type RecordedCall struct {
	URL          string
	MaxBodyBytes int64
}

// RecordingFetcher wraps another fetcher and remembers every call.
type RecordingFetcher struct {
	mu    sync.Mutex
	calls []RecordedCall
}

// NewRecordingFetcher returns an empty recorder. Use Handler to wrap a real fetcher, or Record to
// wrap one manually.
func NewRecordingFetcher() *RecordingFetcher { return &RecordingFetcher{} }

// Handler wraps inner so calls are recorded and then delegated.
func (r *RecordingFetcher) Handler(inner Fetcher) Fetcher {
	return recorderFetcher{recorder: r, inner: inner}
}

// Record notes a call. It is exported so a hand-written fake can use the same recorder.
//
// A nil recorder records nothing, so a fake that does not care about the trace can leave the field
// unset instead of every test having to construct one.
func (r *RecordingFetcher) Record(url string, maxBodyBytes int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, RecordedCall{URL: url, MaxBodyBytes: maxBodyBytes})
}

// Calls returns a copy of the recorded calls.
func (r *RecordingFetcher) Calls() []RecordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RecordedCall(nil), r.calls...)
}

// Count returns how many calls were recorded.
func (r *RecordingFetcher) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// URLs returns just the requested URLs, in order.
func (r *RecordingFetcher) URLs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, c.URL)
	}
	return out
}

type recorderFetcher struct {
	recorder *RecordingFetcher
	inner    Fetcher
}

func (f recorderFetcher) Fetch(ctx context.Context, url string, maxBodyBytes int64) (Response, error) {
	f.recorder.Record(url, maxBodyBytes)
	return f.inner.Fetch(ctx, url, maxBodyBytes)
}
