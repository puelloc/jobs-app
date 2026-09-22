// normalize_test.go: tests for the deliberate normalization stub.
//
// The stub is expected behavior for this iteration, not a placeholder to be
// papered over: these tests pin its contract so the real implementation cannot
// land without deliberately replacing them.
package remoteok

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

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
	// The run's two stale-marking guards fire on these two facts.
	// design: Freshness contract - "Two guards".
	if len(seenIDs) != 0 || len(jobs) != 0 {
		t.Error("both stale-marking guards must fire: seen set empty AND accepted count zero")
	}
}

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

func TestNormalizeStubNeverMentionsJobText(t *testing.T) {
	// design: Observability / Not logged - skip reasons reach stderr, so they
	// must not carry payload content.
	secret := "CompanyNameThatMustNotBeLogged"
	elements := []json.RawMessage{
		json.RawMessage(`{"id":"1","company":"` + secret + `","description":"<p>secret</p>"}`),
	}
	_, _, skipped, err := Normalize(elements)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	for _, sk := range skipped {
		if strings.Contains(sk.Reason, secret) || strings.Contains(sk.Reason, "secret") {
			t.Errorf("skip reason %q leaks payload content", sk.Reason)
		}
	}
}

// TestNormalizeStubAgainstRealCapture runs the stub over the real payload when a
// capture is present, asserting the exact outcome the run lifecycle depends on:
// 100 elements in, 0 jobs, 0 seen ids, 100 skips.
//
// The test skips when the capture is absent so a fresh clone still passes.
func TestNormalizeStubAgainstRealCapture(t *testing.T) {
	const capture = "../../../data/raw/remoteok-2026-09-22.json"
	body, err := os.ReadFile(capture)
	if err != nil {
		t.Skipf("no raw capture at %s: %v", capture, err)
	}

	elements, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse on the real capture: %v", err)
	}
	if len(elements) != 100 {
		t.Fatalf("capture has %d elements, want 100", len(elements))
	}

	jobs, seenIDs, skipped, err := Normalize(elements)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(jobs) != 0 || len(seenIDs) != 0 {
		t.Fatalf("stub produced %d jobs and %d seen ids, want 0 and 0", len(jobs), len(seenIDs))
	}
	if len(skipped) != len(elements) {
		t.Fatalf("got %d skips for %d elements, want one per element", len(skipped), len(elements))
	}
}
