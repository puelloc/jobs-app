// gate.go: the single-job concurrency gate plus stop control, shared by every server-triggered job.
//
// Only one job runs at a time (SQLite's single writer and the shared model host make concurrency
// worse, not faster). The runner also owns stopping the in-flight job: each spawned command is put
// in its own process group, and stop() signals the whole group so the job's Python worker and
// Chromium die with it rather than lingering as orphans.
package api

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// stopGrace is how long SIGTERM is given before the runner escalates to SIGKILL.
const stopGrace = 2 * time.Second

type jobRunner struct {
	mu      sync.Mutex
	running bool
	pgid    int
	stopped bool
	paused  bool
}

func (r *jobRunner) tryAcquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return false
	}
	r.running = true
	r.stopped = false
	r.paused = false
	return true
}

func (r *jobRunner) release() {
	r.mu.Lock()
	r.running = false
	r.pgid = 0
	r.stopped = false
	r.paused = false
	r.mu.Unlock()
}

// register records the running command's process group so stop() can signal it. Called right after
// cmd.Start(); the command must have been started with SysProcAttr{Setpgid: true}.
func (r *jobRunner) register(cmd *exec.Cmd) {
	r.mu.Lock()
	r.pgid = cmd.Process.Pid
	r.mu.Unlock()
}

// stop signals the in-flight job's whole process group (SIGTERM, then SIGKILL after a grace period)
// and flags it stopped so the reaper does not overwrite the cancelled state. It reports whether
// there was a process to signal.
func (r *jobRunner) stop() bool {
	r.mu.Lock()
	pgid := r.pgid
	r.stopped = true
	r.mu.Unlock()
	if pgid == 0 {
		return false
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	go func() {
		time.Sleep(stopGrace)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}()
	return true
}

// isStopped reports whether stop() has been called for the current job, so a reaper can skip its
// own FinishRun and leave the "cancelled" state the stop handler wrote.
func (r *jobRunner) isStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// pause freezes the in-flight job's whole process group (SIGSTOP). The process is suspended in
// place: CPU is freed, memory is retained, and resume() continues it exactly where it was.
func (r *jobRunner) pause() {
	r.mu.Lock()
	pgid := r.pgid
	if pgid == 0 {
		r.mu.Unlock()
		return
	}
	r.paused = true
	r.mu.Unlock()
	_ = syscall.Kill(-pgid, syscall.SIGSTOP)
}

// resume unfreezes the in-flight job's process group (SIGCONT).
func (r *jobRunner) resume() {
	r.mu.Lock()
	if !r.paused || r.pgid == 0 {
		r.mu.Unlock()
		return
	}
	pgid := r.pgid
	r.paused = false
	r.mu.Unlock()
	_ = syscall.Kill(-pgid, syscall.SIGCONT)
}

// isPaused reports whether the current job is frozen.
func (r *jobRunner) isPaused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}
