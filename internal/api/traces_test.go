// traces_test.go: tests for GET /api/traces/{id} against a real temp directory.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"jobsapp/internal/db"
)

// newTraceTestServer returns a router whose traces live under the returned temp directory.
func newTraceTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dir := t.TempDir()
	return NewRouter(database, dir), dir
}

func TestGetTrace_ReturnsEvents(t *testing.T) {
	h, dir := newTraceTestServer(t)
	if err := os.MkdirAll(filepath.Join(dir, "traces"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "{\"event\":\"step\",\"step\":1,\"next_goal\":\"go\"}\n" +
		"{\"event\":\"done\",\"success\":true,\"steps\":1}\n"
	if err := os.WriteFile(filepath.Join(dir, "traces", "42.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/traces/42")
	requireStatus(t, rec, http.StatusOK)

	var got TraceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "42" || !got.Present || len(got.Events) != 2 {
		t.Errorf("id=%q present=%t events=%d, want id=42 present=true events=2",
			got.ID, got.Present, len(got.Events))
	}
}

func TestGetTrace_MissingIsNotError(t *testing.T) {
	h, _ := newTraceTestServer(t)

	rec := do(t, h, http.MethodGet, "/api/traces/nope")
	requireStatus(t, rec, http.StatusOK)

	var got TraceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Present {
		t.Error("present should be false for a missing trace")
	}
	if len(got.Events) != 0 {
		t.Errorf("events should be empty, got %d", len(got.Events))
	}
}

func TestGetTrace_RejectsUnsafeID(t *testing.T) {
	h, _ := newTraceTestServer(t)
	rec := do(t, h, http.MethodGet, "/api/traces/bad!id")
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}
