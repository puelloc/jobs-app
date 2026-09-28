// sweep_test.go: tests for the full-sweep resume point endpoint.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestSweepPosition_Present(t *testing.T) {
	h, _, dataDir := newJobsTestServer(t)
	if err := os.MkdirAll(filepath.Join(dataDir, "sweep"), 0o755); err != nil {
		t.Fatalf("mkdir sweep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "sweep", "resume"), []byte("abbott-laboratories\n"), 0o644); err != nil {
		t.Fatalf("write resume point: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/sweep/position")
	requireStatus(t, rec, http.StatusOK)
	var got SweepPositionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Present || got.Slug != "abbott-laboratories" {
		t.Errorf("got %+v, want present=true slug=abbott-laboratories", got)
	}
}

func TestSweepPosition_NotPresent(t *testing.T) {
	h, _, _ := newJobsTestServer(t)
	rec := do(t, h, http.MethodGet, "/api/sweep/position")
	requireStatus(t, rec, http.StatusOK)
	var got SweepPositionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Present {
		t.Errorf("got present=true, want false (no resume point)")
	}
}
