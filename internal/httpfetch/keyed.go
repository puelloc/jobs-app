// keyed.go: per-host bookkeeping for the limiter.
//
// Two small structures rather than a dependency: a semaphore and a timestamp map, both keyed by
// host, both bounded by the number of hosts in one run (a few thousand), so neither needs eviction.
package httpfetch

import (
	"context"
	"sync"
	"time"
)

// keyedSemaphore serialises work per key. Capacity one is the intended use: two concurrent requests
// to the same origin is what a site notices.
type keyedSemaphore struct {
	mu    sync.Mutex
	slots map[string]chan struct{}
}

func newKeyedSemaphore() *keyedSemaphore {
	return &keyedSemaphore{slots: map[string]chan struct{}{}}
}

// acquire blocks until the key's slot is free, and returns when the context ends instead of waiting
// forever.
//
// The context is checked before the select, not only inside it. When the context is already
// cancelled and the slot is free, both select cases are ready and Go chooses between them at random,
// so roughly half of the acquisitions would succeed and let a request proceed after shutdown. That
// is an intermittent bug, which is worse than a consistent one: it would surface as a handful of
// requests escaping a cancelled run and writing attempts for work that was abandoned.
func (s *keyedSemaphore) acquire(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	slot, ok := s.slots[key]
	if !ok {
		slot = make(chan struct{}, 1)
		s.slots[key] = slot
	}
	s.mu.Unlock()

	select {
	case slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *keyedSemaphore) release(key string) {
	s.mu.Lock()
	slot, ok := s.slots[key]
	s.mu.Unlock()
	if !ok {
		return
	}
	select {
	case <-slot:
	default:
	}
}

// keyedTimes records the last request time per key.
type keyedTimes struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newKeyedTimes() *keyedTimes { return &keyedTimes{m: map[string]time.Time{}} }

func (t *keyedTimes) get(key string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[key]
	return v, ok
}

func (t *keyedTimes) set(key string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[key] = at
}
