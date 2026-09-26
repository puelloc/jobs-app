// rawstore_test.go: tests for the raw-payload capture.
package rawstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fixed instant so filenames are deterministic. The zone is non-UTC on purpose: the name must be
// the UTC date, not the local one.
var testTime = time.Date(2026, 9, 22, 18, 30, 0, 0, time.FixedZone("CDT", -5*60*60))

func TestFilenameUsesSourceAndUTCDate(t *testing.T) {
	got := Filename("wikipedia_sp500", "wiki", testTime)
	want := "wikipedia_sp500-2026-09-22.wiki"
	if got != want {
		t.Errorf("Filename = %q, want %q", got, want)
	}
}

func TestFilenameConvertsToUTC(t *testing.T) {
	// 2026-09-23T02:00Z is still 2026-09-22 locally at UTC-5, so the UTC date must win.
	late := time.Date(2026, 9, 22, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
	got := Filename("wikipedia_sp500", "wiki", late)
	if !strings.Contains(got, "2026-09-23") {
		t.Errorf("Filename = %q, want the UTC date 2026-09-23", got)
	}
}

func TestWriteWritesVerbatim(t *testing.T) {
	dir := t.TempDir()
	body := []byte("line one\nline two\r\n\ttabbed  \n")

	path, written, err := Write(dir, "wikipedia_sp500", "wiki", testTime, body)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !written {
		t.Error("written = false, want true on a fresh directory")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("stored %q, want the bytes verbatim %q", got, body)
	}
}

func TestWriteCreatesRawDir(t *testing.T) {
	dir := t.TempDir()

	path, _, err := Write(dir, "wikipedia_sp400", "wiki", testTime, []byte("x"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(dir, "raw") {
		t.Errorf("payload landed in %s, want %s", filepath.Dir(path), filepath.Join(dir, "raw"))
	}
	if _, err := os.Stat(filepath.Join(dir, "raw")); err != nil {
		t.Errorf("raw directory missing: %v", err)
	}
}

// The first run of a UTC day owns that day's file, and a second must not replace it.
func TestWriteDoesNotOverwriteSameDay(t *testing.T) {
	dir := t.TempDir()

	first, written, err := Write(dir, "wikipedia_sp500", "wiki", testTime, []byte("FIRST"))
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if !written {
		t.Fatal("first Write reported written = false")
	}

	// testTime is 23:30 UTC (18:30 at UTC-5), so adding even one hour crosses midnight UTC and
	// the "collision" would really be a new day. Going backwards stays inside the same UTC date.
	second, written, err := Write(dir, "wikipedia_sp500", "wiki", testTime.Add(-time.Hour), []byte("SECOND"))
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if written {
		t.Error("second Write reported written = true, want false")
	}
	if second != first {
		t.Errorf("second Write returned %q, want the existing %q", second, first)
	}

	got, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "FIRST" {
		t.Errorf("stored %q, want the first payload to survive", got)
	}
}

func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()

	if _, _, err := Write(dir, "wikipedia_sp500", "wiki", testTime, []byte("body")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatalf("read raw dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %s survived a successful write", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("raw dir holds %d entries, want 1", len(entries))
	}
}

func TestWriteDifferentDaysCoexist(t *testing.T) {
	dir := t.TempDir()

	day1, _, err := Write(dir, "wikipedia_sp500", "wiki", testTime, []byte("day1"))
	if err != nil {
		t.Fatalf("day 1: %v", err)
	}
	day2, written, err := Write(dir, "wikipedia_sp500", "wiki", testTime.Add(24*time.Hour), []byte("day2"))
	if err != nil {
		t.Fatalf("day 2: %v", err)
	}
	if !written {
		t.Error("a different UTC day reported written = false")
	}
	if day1 == day2 {
		t.Errorf("both days resolved to %q", day1)
	}
}

// Two sources on the same day are separate captures.
func TestWriteSeparatesSourcesOnTheSameDay(t *testing.T) {
	dir := t.TempDir()

	sp500, _, err := Write(dir, "wikipedia_sp500", "wiki", testTime, []byte("500"))
	if err != nil {
		t.Fatalf("sp500: %v", err)
	}
	sp400, written, err := Write(dir, "wikipedia_sp400", "wiki", testTime, []byte("400"))
	if err != nil {
		t.Fatalf("sp400: %v", err)
	}
	if !written {
		t.Error("a second source on the same day reported written = false")
	}
	if sp500 == sp400 {
		t.Errorf("both sources resolved to %q", sp500)
	}
}

// A different extension is a different capture, which is what lets a JSON feed and a wikitext page
// share a data directory.
func TestWriteSeparatesExtensions(t *testing.T) {
	dir := t.TempDir()

	jsonPath, _, err := Write(dir, "wikipedia_sp500", "json", testTime, []byte("{}"))
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	wikiPath, written, err := Write(dir, "wikipedia_sp500", "wiki", testTime, []byte("text"))
	if err != nil {
		t.Fatalf("wiki: %v", err)
	}
	if !written {
		t.Error("a second extension reported written = false")
	}
	if jsonPath == wikiPath {
		t.Errorf("both extensions resolved to %q", jsonPath)
	}
}

func TestWriteRejectsMissingSourceOrExtension(t *testing.T) {
	dir := t.TempDir()

	if _, _, err := Write(dir, "", "wiki", testTime, []byte("x")); err == nil {
		t.Error("Write accepted an empty source, want an error")
	}
	if _, _, err := Write(dir, "wikipedia_sp500", "", testTime, []byte("x")); err == nil {
		t.Error("Write accepted an empty extension, want an error")
	}
}

// An existing path that is not a regular file must fail rather than being treated as a collision,
// or the run would proceed believing a capture is on disk.
func TestWriteReportsAnErrorWhenTheNameIsADirectory(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "raw")
	if err := os.MkdirAll(filepath.Join(rawDir, Filename("wikipedia_sp500", "wiki", testTime)), 0o755); err != nil {
		t.Fatalf("pre-create directory: %v", err)
	}

	if _, _, err := Write(dir, "wikipedia_sp500", "wiki", testTime, []byte("x")); err == nil {
		t.Error("Write treated a directory as a same-day collision, want an error")
	}
}
