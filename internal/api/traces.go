// traces.go: the handler behind GET /api/traces/{id}.
//
// A browser-use agent run can write a live trace (one JSON object per step) to
// <dataDir>/traces/<id>.jsonl via the worker's trace_file. This endpoint reads that file back, one
// event per array element, so the UI can poll it while a run is in flight.
package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// traceIDRE limits a trace id to a single safe filename component, so it can be joined onto the
// traces directory without ever escaping it.
var traceIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// handleGetTrace serves GET /api/traces/{id}: the agent-trace JSONL at <dataDir>/traces/<id>.jsonl,
// parsed into an array of events. A missing file is present=false with no events, not an error -
// "the run has not written a trace yet" is a normal state for a poller.
func handleGetTrace(dataDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !traceIDRE.MatchString(id) {
			writeError(w, http.StatusBadRequest, codeBadRequest, "trace id must be a safe filename")
			return
		}

		path := filepath.Join(dataDir, "traces", id+".jsonl")
		events, err := readTraceEvents(path)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusOK, TraceResponse{ID: id, Present: false, Events: []json.RawMessage{}})
				return
			}
			writeInternalError(w, fmt.Errorf("read trace %s: %w", id, err))
			return
		}
		writeJSON(w, http.StatusOK, TraceResponse{ID: id, Present: true, Events: events})
	}
}

// readTraceEvents reads a JSONL file and returns each line as a raw JSON event, skipping blank lines
// and a trailing partial line (the worker flushes after every event, so a partial line can only be
// the one being written right now).
func readTraceEvents(path string) ([]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	events := make([]json.RawMessage, 0)
	scanner := bufio.NewScanner(f)
	// A "done" event carries the final answer, which can be long; the default 64KB scanner limit is
	// not enough.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			continue
		}
		// Copy: the scanner reuses its buffer, so an aliased RawMessage would be overwritten.
		event := make([]byte, len(line))
		copy(event, line)
		events = append(events, json.RawMessage(event))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
