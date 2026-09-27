// gate.go: the single-job concurrency gate shared by every server-triggered job.
package api

import "sync"

// jobGate serializes jobs within one server process: only one runs at a time. The full sweep goes
// through cmd/batch, which is sequential by construction; this is the same boundary for the
// per-company scrape trigger and the generic job trigger, so neither the UI nor a script can fan
// out into concurrent browser agents (which would contend on SQLite's single writer and the shared
// model host). A process already in flight makes a new trigger a 409 conflict.
type jobGate struct {
	mu      sync.Mutex
	running bool
}

func (g *jobGate) tryAcquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return false
	}
	g.running = true
	return true
}

func (g *jobGate) release() {
	g.mu.Lock()
	g.running = false
	g.mu.Unlock()
}
