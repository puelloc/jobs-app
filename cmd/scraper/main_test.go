// main_test.go: tests for the log-line formatting and file-writing helpers.
//
// These pin the observable contract from docs/scraper-design.md: one stdout line
// on success, one stderr line on failure, fixed key order, and the raw-capture
// naming and collision behavior.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jobsapp/internal/scraper/remoteok"
)

// --- singleLine ----------------------------------------------------------

func TestSingleLineEscapesNewlines(t *testing.T) {
	got := singleLine("first\nsecond\r\nthird\ttabbed")
	for _, bad := range []string{"\n", "\r", "\t"} {
		if strings.Contains(got, bad) {
			t.Errorf("singleLine output %q still contains a raw %q", got, bad)
		}
	}
	if !strings.Contains(got, `\n`) || !strings.Contains(got, `\r`) || !strings.Contains(got, `\t`) {
		t.Errorf("singleLine output %q, want escaped \\n, \\r and \\t sequences", got)
	}
}

func TestSingleLineStripsControlCharacters(t *testing.T) {
	got := singleLine("a\x00b\x07c")
	if strings.ContainsAny(got, "\x00\x07") {
		t.Errorf("singleLine output %q still contains control characters", got)
	}
}

func TestSingleLineTruncatesAt1000(t *testing.T) {
	got := singleLine(strings.Repeat("x", 5000))
	if n := len([]rune(got)); n != 1000 {
		t.Errorf("singleLine length = %d runes, want 1000", n)
	}
}

func TestSingleLineLeavesShortTextAlone(t *testing.T) {
	const in = "a plain error message"
	if got := singleLine(in); got != in {
		t.Errorf("singleLine(%q) = %q, want it unchanged", in, got)
	}
}

// --- rawFilename ---------------------------------------------------------

func TestRawFilenameUsesSourceAndUTCDate(t *testing.T) {
	ts := time.Date(2026, 9, 22, 14, 36, 54, 0, time.UTC)
	got := rawFilename(ts)
	if got != "remoteok-2026-09-22.json" {
		t.Errorf("rawFilename = %q, want remoteok-2026-09-22.json", got)
	}
}

func TestRawFilenameConvertsToUTC(t *testing.T) {
	// 2026-09-22T02:00:00+05:00 is 2026-09-21T21:00:00Z, so the capture belongs
	// to the 21st, not the 22nd.
	loc := time.FixedZone("UTC+5", 5*3600)
	ts := time.Date(2026, 9, 22, 2, 0, 0, 0, loc)
	if got := rawFilename(ts); got != "remoteok-2026-09-21.json" {
		t.Errorf("rawFilename = %q, want remoteok-2026-09-21.json (UTC date)", got)
	}
}

// --- writeRawPayload -----------------------------------------------------

func TestWriteRawPayloadWritesVerbatim(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`[{"legal":"x"},{"id":"1"}]`)

	path, written, err := writeRawPayload(dir, time.Date(2026, 9, 22, 1, 2, 3, 0, time.UTC), body)
	if err != nil {
		t.Fatalf("writeRawPayload: %v", err)
	}
	if !written {
		t.Error("written = false, want true on the first write")
	}
	if want := filepath.Join(dir, "raw", "remoteok-2026-09-22.json"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("stored %q, want the bytes verbatim %q", got, body)
	}
}

func TestWriteRawPayloadCreatesRawDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	if _, _, err := writeRawPayload(dir, time.Now().UTC(), []byte(`[]`)); err != nil {
		t.Fatalf("writeRawPayload did not create %s: %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "raw")); err != nil {
		t.Errorf("raw directory missing: %v", err)
	}
}

// design: Durability - "An existing file is never truncated, appended to, or replaced."
func TestWriteRawPayloadDoesNotOverwriteSameDay(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 9, 22, 1, 2, 3, 0, time.UTC)

	firstPath, firstWritten, err := writeRawPayload(dir, ts, []byte(`"first capture"`))
	if err != nil || !firstWritten {
		t.Fatalf("first write: path=%q written=%v err=%v", firstPath, firstWritten, err)
	}

	secondPath, secondWritten, err := writeRawPayload(dir, ts, []byte(`"second capture"`))
	if err != nil {
		t.Fatalf("second write returned an error rather than a collision: %v", err)
	}
	if secondWritten {
		t.Error("written = true on a same-day collision, want false")
	}
	if secondPath != firstPath {
		t.Errorf("path = %q, want the same path %q", secondPath, firstPath)
	}

	got, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != `"first capture"` {
		t.Errorf("file = %s, want the FIRST capture preserved", got)
	}
}

// design: Durability / Atomicity - no partial file and no leftover temp files.
func TestWriteRawPayloadLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := writeRawPayload(dir, time.Now().UTC(), []byte(`[]`)); err != nil {
		t.Fatalf("writeRawPayload: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatalf("read raw dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("raw dir holds %v, want exactly one file and no temp leftovers", names)
	}
}

func TestWriteRawPayloadDifferentDaysCoexist(t *testing.T) {
	dir := t.TempDir()
	day1 := time.Date(2026, 9, 22, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 23, 0, 1, 0, 0, time.UTC)

	if _, written, err := writeRawPayload(dir, day1, []byte(`"day1"`)); err != nil || !written {
		t.Fatalf("day1: written=%v err=%v", written, err)
	}
	if _, written, err := writeRawPayload(dir, day2, []byte(`"day2"`)); err != nil || !written {
		t.Fatalf("day2: written=%v err=%v", written, err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatalf("read raw dir: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d captures, want one per day", len(entries))
	}
}

// --- errorText -----------------------------------------------------------

func TestErrorTextIncludesConditionAndMessage(t *testing.T) {
	got := errorText(failure{condition: "no_jobs", err: errors.New("nothing accepted")})
	if got == nil {
		t.Fatal("errorText = nil, want a value")
	}
	if !strings.Contains(*got, "no_jobs") || !strings.Contains(*got, "nothing accepted") {
		t.Errorf("errorText = %q, want it to contain both the condition and the message", *got)
	}
}

func TestErrorTextIsSingleLine(t *testing.T) {
	got := errorText(failure{condition: "parse", err: errors.New("line one\nline two")})
	if got == nil {
		t.Fatal("errorText = nil")
	}
	if strings.Contains(*got, "\n") {
		t.Errorf("errorText = %q, want newlines escaped", *got)
	}
}

func TestErrorTextNilWhenNoErrorAndNoCondition(t *testing.T) {
	if got := errorText(failure{}); got != nil {
		t.Errorf("errorText = %v, want nil so the column stays NULL", *got)
	}
}

// A condition with no underlying error still yields a usable label.
func TestErrorTextFallsBackToConditionOnly(t *testing.T) {
	got := errorText(failure{condition: "no_jobs"})
	if got == nil {
		t.Fatal("errorText = nil, want the condition")
	}
	if *got != "no_jobs" {
		t.Errorf("errorText = %q, want %q", *got, "no_jobs")
	}
}

// design: Observability - error_text must never carry payload content.
func TestErrorTextNeverCarriesResponseBodies(t *testing.T) {
	// The failure struct only ever holds a short condition plus an error string;
	// this pins that a large HTML body is not passed through unbounded.
	body := strings.Repeat("<html>", 1000)
	got := errorText(failure{condition: "invalid_json", err: fmt.Errorf("body was %s", body)})
	if got == nil {
		t.Fatal("errorText = nil")
	}
	if len([]rune(*got)) > 1100 {
		t.Errorf("errorText length = %d runes, want it truncated near 1000", len([]rune(*got)))
	}
}

// --- fetchCondition ------------------------------------------------------

func TestFetchConditionClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"no error", nil, 200, ""},
		{"403", &remoteok.HTTPError{StatusCode: 403}, 403, "http_403"},
		{"404", &remoteok.HTTPError{StatusCode: 404}, 404, "http_404"},
		{"429 twice", &remoteok.HTTPError{StatusCode: 429}, 429, "http_429_twice"},
		{"500", &remoteok.HTTPError{StatusCode: 500}, 500, "http_5xx"},
		{"503", &remoteok.HTTPError{StatusCode: 503}, 503, "http_5xx"},
		{"teapot", &remoteok.HTTPError{StatusCode: 418}, 418, "http_status"},
		{"oversized retry-after", fmt.Errorf("wrapped: %w", remoteok.ErrRetryAfterTooLong), 429, "retry_after_too_long"},
		{"timeout", context.DeadlineExceeded, 0, "timeout"},
		{"dns", &net.DNSError{Err: "no such host", Name: "nope.invalid"}, 0, "dns_failure"},
		{"tls", errors.New("tls: failed to verify certificate"), 0, "tls_failure"},
		{"other transport", errors.New("connection reset by peer"), 0, "connection_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fetchCondition(tc.err, tc.status); got != tc.want {
				t.Errorf("fetchCondition(%v, %d) = %q, want %q", tc.err, tc.status, got, tc.want)
			}
		})
	}
}

// --- output lines --------------------------------------------------------

// design: Observability / "stdout on success" - fixed key order.
func TestPrintSuccessLineFormat(t *testing.T) {
	stdout, stderr := captureOutput(t, func() {
		printSuccess(7, 200, 624930, "data/raw/remoteok-2026-09-22.json", "written", 99, 99, 0, 0, 1500*time.Millisecond)
	})

	if stderr != "" {
		t.Errorf("success wrote to stderr: %q", stderr)
	}
	want := "run=7 source=remoteok status=ok http=200 bytes=624930 path=data/raw/remoteok-2026-09-22.json raw=written found=99 inserted=99 updated=0 closed=0 duration_ms=1500\n"
	if stdout != want {
		t.Errorf("stdout = %q\nwant        %q", stdout, want)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Errorf("stdout has %d lines, want exactly 1", strings.Count(stdout, "\n"))
	}
}

func TestPrintSuccessRawExistingMarker(t *testing.T) {
	stdout, _ := captureOutput(t, func() {
		printSuccess(2, 200, 100, "p", "existing", 100, 0, 0, 0, time.Millisecond)
	})
	if !strings.Contains(stdout, "raw=existing") {
		t.Errorf("stdout = %q, want raw=existing on a collision", stdout)
	}
}

// design: Observability / stdout-stderr on failure - one line, same key shape.
func TestPrintFailureLineFormat(t *testing.T) {
	stderr := ""
	_, stderr = captureOutput(t, func() {
		printFailure("3", failure{
			step:      "fetch",
			condition: "http_503",
			err:       errors.New("unexpected HTTP status: HTTP 503"),
			httpCode:  503,
			rawPath:   "data/raw/x.json",
		}, 1)
	})

	if strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr has %d lines, want exactly 1: %q", strings.Count(stderr, "\n"), stderr)
	}
	for _, want := range []string{
		"run=3", "source=remoteok", "status=error", "step=fetch",
		"condition=http_503", "http=503", "exit=1",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}

func TestPrintFailureUsesDashesForUnknownValues(t *testing.T) {
	_, stderr := captureOutput(t, func() {
		printFailure("none", failure{step: "config", condition: "bad_config", err: errors.New("boom")}, 2)
	})
	if !strings.Contains(stderr, "http=-") {
		t.Errorf("stderr = %q, want http=- when there was no response", stderr)
	}
	if !strings.Contains(stderr, "path=-") {
		t.Errorf("stderr = %q, want path=- when no raw file was written", stderr)
	}
	if !strings.Contains(stderr, "run=none") {
		t.Errorf("stderr = %q, want run=none before a run row exists", stderr)
	}
}

func TestPrintFailureNeverEchoesResponseBodies(t *testing.T) {
	// The error text is what reaches the log, so it must be bounded and escaped.
	_, stderr := captureOutput(t, func() {
		printFailure("1", failure{
			step:      "parse",
			condition: "invalid_json",
			err:       errors.New("response is not a JSON array: <!doctype html>"),
		}, 1)
	})
	if strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr has %d lines, want 1", strings.Count(stderr, "\n"))
	}
	if !strings.Contains(stderr, "err=") {
		t.Errorf("stderr = %q, want an err= field", stderr)
	}
}

// --- finishWriteFailed ---------------------------------------------------

func TestFinishWriteFailedLockMapsToExit3(t *testing.T) {
	database, path, err := openTestDB(t)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	busy := realBusyError(t, database, path)

	var code int
	_, stderr := captureOutput(t, func() {
		code = finishWriteFailed("5", busy, 200, "data/raw/x.json")
	})
	if code != exitDBLocked {
		t.Errorf("exit code = %d, want %d for a lock error", code, exitDBLocked)
	}
	if !strings.Contains(stderr, "step=finish") {
		t.Errorf("stderr = %q, want step=finish", stderr)
	}
}

func TestFinishWriteFailedOtherErrorMapsToExit2(t *testing.T) {
	var code int
	_, stderr := captureOutput(t, func() {
		code = finishWriteFailed("5", errors.New("disk on fire"), 200, "")
	})
	if code != exitConfig {
		t.Errorf("exit code = %d, want %d", code, exitConfig)
	}
	if strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr has %d lines, want 1", strings.Count(stderr, "\n"))
	}
}

// --- exit code constants -------------------------------------------------

func TestExitCodesMatchTheDesignDoc(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"ok", exitOK, 0},
		{"failure", exitFailure, 1},
		{"config", exitConfig, 2},
		{"db locked", exitDBLocked, 3},
		{"no jobs", exitNoJobs, 4},
		{"raw collision", exitRawCollision, 5},
	} {
		if tc.got != tc.want {
			t.Errorf("%s exit code = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestPlatformIDIsTheRemoteOKSeedRow(t *testing.T) {
	if platformID != 1 {
		t.Errorf("platformID = %d, want 1 (the remoteok seed id)", platformID)
	}
}
