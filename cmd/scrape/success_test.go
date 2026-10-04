// success_test.go: the logging that makes a *successful* run learnable, not just a failed one.
//
// A company can be resolved correctly by a process that took nineteen steps, wandered onto pages it
// should not have visited, and was accepted by nobody's judgement. Every field tested here exists
// because that run would otherwise be indistinguishable from a clean three-step one - and it is the
// run worth finding.

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQualityNoteNamesTheShapesThatResolveWithoutWorking(t *testing.T) {
	cases := []struct {
		name            string
		resolution      string
		remoteConfirmed bool
		found           int
		inserted        int
		skippedNonUS    int
		want            string
	}{
		{
			// The cached URL was reused and yielded nothing: either a genuinely empty board or a URL that
			// has gone stale, and only this tells you to reconsider the TTL.
			name: "cached url yielded nothing", resolution: "cache", remoteConfirmed: true,
			found: 0, want: "cached_url_yielded_nothing",
		},
		{
			name: "agent said remote, fetch found nothing", resolution: "agent", remoteConfirmed: true,
			found: 0, want: "agent_confirmed_remote_but_fetch_found_nothing",
		},
		{
			name: "agent said remote, every posting was foreign", resolution: "agent", remoteConfirmed: true,
			found: 12, inserted: 0, skippedNonUS: 12,
			want: "agent_confirmed_remote_but_all_postings_non_us",
		},
		{
			name: "agent said remote, nothing stored and nothing filtered", resolution: "agent",
			remoteConfirmed: true, found: 4, inserted: 0, skippedNonUS: 0,
			want: "agent_confirmed_remote_but_nothing_stored",
		},
		{
			// The healthy case must stay quiet, or the field becomes noise nobody reads.
			name: "a clean run says nothing", resolution: "agent", remoteConfirmed: true,
			found: 5, inserted: 2, skippedNonUS: 3, want: "",
		},
		{
			// An agent path that found nothing is already reported by the no_remote_roles skip.
			name: "agent found nothing", resolution: "agent", remoteConfirmed: false, found: 0, want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := qualityNote(tc.resolution, tc.remoteConfirmed, tc.found, tc.inserted, tc.skippedNonUS)
			if got != tc.want {
				t.Errorf("qualityNote(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJudgementNoteReadsTheUsefulField(t *testing.T) {
	cases := map[string]string{
		`{"verdict":"failure","reasoning":"never opened the search page"}`: "failure",
		`{"reasoning":"the agent skipped the filter"}`:                     "the agent skipped the filter",
		`{"failure_reason":"goal not met"}`:                                "goal not met",
		`"a bare string"`:                                                  `"a bare string"`,
		`null`:                                                             "",
		``:                                                                 "",
	}
	for raw, want := range cases {
		if got := judgementNote(json.RawMessage(raw)); got != want {
			t.Errorf("judgementNote(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestJudgementNoteIsBounded(t *testing.T) {
	// The judge can be verbose; one company must not be able to dominate a log line.
	long := `{"reasoning":"` + strings.Repeat("word ", 200) + `"}`
	got := judgementNote(json.RawMessage(long))
	if len(got) > 204 {
		t.Errorf("judgementNote returned %d characters, want it bounded", len(got))
	}
}

func TestFirstCharsCollapsesWhitespaceAndTruncates(t *testing.T) {
	if got := firstChars("a\n\n  b\tc", 50); got != "a b c" {
		t.Errorf("firstChars collapsed to %q, want %q", got, "a b c")
	}
	if got := firstChars("abcdefghij", 4); got != "abcd..." {
		t.Errorf("firstChars = %q, want abcd...", got)
	}
}

func TestOrSecondsPrefersTheWorkersOwnMeasurement(t *testing.T) {
	started := time.Now().Add(-10 * time.Second)
	// A worker that reports its own elapsed time wins: it excludes process startup, which the parent's
	// wall clock cannot.
	if got := orSeconds(3.5, started); got != 3.5 {
		t.Errorf("orSeconds(3.5, ...) = %v, want 3.5", got)
	}
	// With nothing reported, the parent's elapsed time is the only number there is.
	if got := orSeconds(0, started); got < 9 {
		t.Errorf("orSeconds(0, ...) = %v, want ~10", got)
	}
}

func TestRound1(t *testing.T) {
	if got := round1(12.3456); got != 12.3 {
		t.Errorf("round1(12.3456) = %v, want 12.3", got)
	}
}
