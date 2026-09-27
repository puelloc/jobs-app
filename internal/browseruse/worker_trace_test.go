// worker_trace_test.go: tests for the trace_file plumbing through the worker request wire.
package browseruse

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWireRequestTraceFile(t *testing.T) {
	payload, err := json.Marshal(toWireRequest(Request{
		Mode: ModeAgent, URL: "https://example.com", TraceFile: "/tmp/t.jsonl",
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"trace_file":"/tmp/t.jsonl"`) {
		t.Errorf("wire request missing trace_file: %s", payload)
	}

	// An empty TraceFile is omitted, so a render-only request stays byte-identical to before.
	empty, err := json.Marshal(toWireRequest(Request{Mode: ModeRender, URL: "https://example.com"}))
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(empty), "trace_file") {
		t.Errorf("trace_file should be omitted when empty: %s", empty)
	}
}
