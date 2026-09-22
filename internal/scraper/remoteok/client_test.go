// client_test.go: offline unit tests for Parse and the Normalize stub.
//
// No network access and no on-disk fixtures: every input is an inline JSON
// string. Fetch is deliberately untested this iteration (the build spec excludes
// it, and it would require either a live endpoint or an HTTP test server).
package remoteok

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The response array is heterogeneous: element 0 is the legal notice and carries
// no id, elements 1 and 2 are jobs.
//
// TODO(mapping): this shape is assumed, not observed. It must be replaced with a
// real captured payload once data/raw/remoteok-*.json exists.
const mixedArrayBody = `[
  {"legal": "RemoteOK API terms and attribution notice"},
  {"id": "1001", "position": "Go Engineer"},
  {"id": "1002", "position": "Backend Engineer"}
]`

func TestParseReturnsEveryElement(t *testing.T) {
	elements, err := Parse([]byte(mixedArrayBody))
	if err != nil {
		t.Fatalf("Parse returned error for a valid array: %v", err)
	}
	if len(elements) != 3 {
		t.Fatalf("Parse returned %d elements, want 3", len(elements))
	}
	// Parse must not skip the notice: skipping is normalization's job
	// (design: Run lifecycle step 6).
	if got := strings.TrimSpace(string(elements[0])); !strings.Contains(got, "legal") {
		t.Errorf("element 0 = %s, want the untouched legal notice", got)
	}
	if got := strings.TrimSpace(string(elements[1])); !strings.Contains(got, "1001") {
		t.Errorf("element 1 = %s, want the first job verbatim", got)
	}
}

func TestParseRejectsNonArray(t *testing.T) {
	if _, err := Parse([]byte(`{"id": "1001"}`)); err == nil {
		t.Fatal("Parse accepted a JSON object, want an error requiring an array")
	}
	if _, err := Parse([]byte(`not json at all`)); err == nil {
		t.Fatal("Parse accepted invalid JSON, want an error")
	}
}

func TestParseEmptyArray(t *testing.T) {
	elements, err := Parse([]byte(`[]`))
	if err != nil {
		t.Fatalf("Parse returned error for an empty array: %v", err)
	}
	if len(elements) != 0 {
		t.Fatalf("Parse returned %d elements, want 0", len(elements))
	}
}

func TestNormalizeStubAcceptsNothing(t *testing.T) {
	elements := []json.RawMessage{
		json.RawMessage(`{"legal": "notice"}`),
		json.RawMessage(`{"id": "1001"}`),
		json.RawMessage(`{"id": "1002"}`),
	}

	jobs, seenIDs, skipped, err := Normalize(elements)
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("Normalize returned %d jobs, want 0 (stub accepts nothing)", len(jobs))
	}
	if len(seenIDs) != 0 {
		t.Errorf("Normalize returned %d seen ids, want 0 (stub extracts nothing)", len(seenIDs))
	}
	if len(skipped) != 3 {
		t.Fatalf("Normalize returned %d skips, want 3 (one per element)", len(skipped))
	}
	for i, sk := range skipped {
		if sk.Index != i {
			t.Errorf("skips[%d].Index = %d, want %d", i, sk.Index, i)
		}
		if !strings.Contains(sk.Reason, "normalizer not implemented") {
			t.Errorf("skips[%d].Reason = %q, want it to mention the stub", i, sk.Reason)
		}
	}
	// The run's two stale-marking guards fire on these two facts, so they are
	// worth asserting explicitly.
	// design: Freshness contract - "Two guards".
	if len(seenIDs) != 0 || len(jobs) != 0 {
		t.Error("both stale-marking guards must fire: seen set empty AND accepted count zero")
	}
}

// TestNormalizeStubReturnsNonNilEmpty guards against nil-vs-empty surprises in
// the caller: main uses len() on both values.
func TestNormalizeStubReturnsNonNilEmpty(t *testing.T) {
	jobs, seenIDs, skipped, err := Normalize(nil)
	if err != nil {
		t.Fatalf("Normalize(nil) returned error: %v", err)
	}
	if jobs == nil {
		t.Error("jobs is nil, want an empty slice")
	}
	if seenIDs == nil {
		t.Error("seenIDs is nil, want an empty map")
	}
	if skipped == nil {
		t.Error("skipped is nil, want an empty slice")
	}
	if !reflect.DeepEqual(skipped, []NormalizeSkip{}) {
		t.Errorf("skipped = %#v, want an empty slice", skipped)
	}
}
