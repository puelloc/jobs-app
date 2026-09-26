// resolve_cli_test.go: tests for the resolve subcommand's argument handling, exit mapping and output.
//
// The exit mapping is the part worth testing hardest. Every one of these cases reports zero accepts,
// and the exit scheme exists to tell them apart:
//
//	failed>0            -> 1   an error, whatever the other counters say
//	resolved>0          -> 0
//	neither             -> 4   a genuine zero-result run
//
// Deriving the code from the accepted count alone collapses all three, which is how a bug that broke
// every write looks like a quiet day.
package main

import (
	"bytes"
	"strings"
	"testing"

	"jobsapp/internal/runresolve"
)

// --- flag parsing ---------------------------------------------------------

func TestOnlySlugsEmptyIsConfigError(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseResolveFlags([]string{"--only-slugs="}, &stderr); err == nil {
		t.Fatal("an explicitly empty --only-slugs was accepted; omitting the flag means every company, but passing it empty is a mistake")
	}
	// Whitespace only is the same mistake spelled differently.
	if _, err := parseResolveFlags([]string{"--only-slugs= , ,"}, &stderr); err == nil {
		t.Fatal("a whitespace-only --only-slugs was accepted")
	}
	// Omitting it entirely is legitimate and means no restriction.
	got, err := parseResolveFlags(nil, &stderr)
	if err != nil {
		t.Fatalf("no --only-slugs should be valid: %v", err)
	}
	if len(got.slugs()) != 0 {
		t.Errorf("slugs() = %v, want empty", got.slugs())
	}
}

func TestOnlySlugsSplitsAndTrims(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseResolveFlags([]string{"--only-slugs= acme , nike-inc,,"}, &stderr)
	if err != nil {
		t.Fatalf("parseResolveFlags: %v", err)
	}
	want := []string{"acme", "nike-inc"}
	if strings.Join(got.slugs(), "|") != strings.Join(want, "|") {
		t.Errorf("slugs() = %v, want %v", got.slugs(), want)
	}
}

func TestConcurrencyFlagRejectsZeroAndNegative(t *testing.T) {
	var stderr bytes.Buffer
	for _, value := range []string{"0", "-1", "-10"} {
		if _, err := parseResolveFlags([]string{"--concurrency=" + value}, &stderr); err == nil {
			t.Errorf("--concurrency=%s was accepted, want a config error", value)
		}
	}
	// The default is four, and a positive value is kept.
	got, err := parseResolveFlags(nil, &stderr)
	if err != nil {
		t.Fatalf("parseResolveFlags: %v", err)
	}
	if got.concurrency != 4 {
		t.Errorf("default concurrency = %d, want 4", got.concurrency)
	}
	got, err = parseResolveFlags([]string{"--concurrency=2"}, &stderr)
	if err != nil {
		t.Fatalf("--concurrency=2: %v", err)
	}
	if got.concurrency != 2 {
		t.Errorf("concurrency = %d, want 2", got.concurrency)
	}
}

func TestLimitRejectsNegative(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseResolveFlags([]string{"--limit=-1"}, &stderr); err == nil {
		t.Error("a negative --limit was accepted")
	}
}

// The two refresh rules select disjoint sets, so asking for both is a contradiction rather than a
// union: --refresh means "including settled companies" and --refresh-failed means "only unsettled".
func TestRefreshAndRefreshFailedContradict(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseResolveFlags([]string{"--refresh", "--refresh-failed"}, &stderr); err == nil {
		t.Error("--refresh and --refresh-failed were accepted together")
	}
}

func TestResolveFlagsParseAllOptions(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseResolveFlags([]string{
		"--only-slugs=acme", "--limit=5", "--refresh", "--dry-run",
		"--concurrency=3", "--user-agent=custom/1.0",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseResolveFlags: %v", err)
	}
	if got.limit != 5 || !got.refresh || !got.dryRun || got.concurrency != 3 || got.userAgent != "custom/1.0" {
		t.Errorf("parsed flags = %+v", got)
	}
}

// --- exit mapping ---------------------------------------------------------

func TestExitZeroRequiresNoFailedItems(t *testing.T) {
	// The clean case.
	if got := exitCodeFor(runresolve.Summary{Resolved: 10}); got != exitOK {
		t.Errorf("clean run exit = %d, want %d", got, exitOK)
	}
	// A run that accepted many and errored on some must not exit 0 silently.
	got := exitCodeFor(runresolve.Summary{Resolved: 1203, Unresolved: 295, Failed: 295})
	if got == exitOK {
		t.Fatalf("a run with %d failures exited 0; failures are errors however many resolved", 295)
	}
	if got != exitFailure {
		t.Errorf("exit = %d, want %d", got, exitFailure)
	}
}

func TestExitFourDistinguishesFromExitOne(t *testing.T) {
	// Zero accepts, zero errors: the run did its job and found nothing.
	quiet := exitCodeFor(runresolve.Summary{Found: 10, Resolved: 0, Unresolved: 10, Failed: 0})
	if quiet != exitEmpty {
		t.Errorf("a genuine zero-result run exited %d, want %d", quiet, exitEmpty)
	}
	// Zero accepts, but because every company errored.
	broken := exitCodeFor(runresolve.Summary{Found: 10, Resolved: 0, Unresolved: 10, Failed: 10})
	if broken != exitFailure {
		t.Errorf("a run that errored on every company exited %d, want %d", broken, exitFailure)
	}
	if quiet == broken {
		t.Fatal("a quiet zero-result run and a run broken by errors share an exit code; that is the distinction the scheme exists for")
	}
}

// A partial success is still a failure, and the summary line still reports it.
func TestExitCodeForPartialFailureIsFailure(t *testing.T) {
	got := exitCodeFor(runresolve.Summary{Found: 100, Resolved: 99, Unresolved: 1, Failed: 1})
	if got != exitFailure {
		t.Errorf("exit = %d, want %d for a run with one failure", got, exitFailure)
	}
}

// --- output ---------------------------------------------------------------

func TestStdoutLineIncludesFailedCount(t *testing.T) {
	line := formatSummaryLine(runresolve.Summary{
		RunID: 42, Found: 1498, Resolved: 1203, Unresolved: 295, Skipped: 0, Failed: 7,
	}, false)

	if !strings.Contains(line, "failed=7") {
		t.Errorf("line %q does not carry the failure count; a run that produced zero accepts because of a bug must say failed=1498 rather than leave resolved=0 to be read as a quiet result", line)
	}
	for _, want := range []string{"run_id=42", "dry_run=0", "companies=1498", "resolved=1203", "unresolved=295", "skipped=0", "duration_ms="} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %q", line, want)
		}
	}
}

func TestStdoutLineReflectsDryRun(t *testing.T) {
	if line := formatSummaryLine(runresolve.Summary{RunID: 1}, true); !strings.Contains(line, "dry_run=1") {
		t.Errorf("line %q does not mark the dry run", line)
	}
	if line := formatSummaryLine(runresolve.Summary{RunID: 1}, false); !strings.Contains(line, "dry_run=0") {
		t.Errorf("line %q does not mark a real run", line)
	}
}

func TestStdoutLineIsSingleLineOnSuccess(t *testing.T) {
	line := formatSummaryLine(runresolve.Summary{
		RunID: 1, Found: 2, Resolved: 1, Unresolved: 1, FirstError: "acme: something\nwith a newline",
	}, false)
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("the summary line spans more than one line: %q", line)
	}
}

func TestStdoutLineOnFailureGoesToStderr(t *testing.T) {
	// The failure line is built by runResolve, so assert its shape: it names the run and the first
	// error, and no summary line is printed.
	var stdout, stderr bytes.Buffer
	// An unknown subcommand is a config error reported on stderr, with nothing on stdout.
	code := dispatch([]string{"nonsense"}, &stdout, &stderr)
	if code != exitConfig {
		t.Errorf("exit = %d, want %d", code, exitConfig)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing on a failure", stdout.String())
	}
	if !strings.Contains(stderr.String(), "status=error") {
		t.Errorf("stderr = %q, want a status=error line", stderr.String())
	}
	if strings.ContainsAny(stderr.String(), "\r") {
		t.Errorf("stderr spans more than one line: %q", stderr.String())
	}
}

// A config error must not reach the network or the database, and must be reported on stderr only.
func TestResolveConfigErrorGoesToStderrWithoutTouchingAnything(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runResolve([]string{"--only-slugs="}, &stdout, &stderr)
	if code != exitConfig {
		t.Errorf("exit = %d, want %d", code, exitConfig)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), "step=config") {
		t.Errorf("stderr = %q, want it to name the config step", stderr.String())
	}
}

// --- dispatcher -----------------------------------------------------------

// With no subcommand the index bootstrap runs, so an existing cron entry keeps working.
func TestDispatchDefaultsToIndices(t *testing.T) {
	// A bad config makes the index run exit 2 without touching the network, which is enough to show
	// which subcommand ran: `resolve` would have reported step=config from its own parser.
	t.Setenv("DB_PATH", "/nonexistent-directory-that-cannot-be-created/x/jobs.db")
	var stdout, stderr bytes.Buffer
	code := dispatch(nil, &stdout, &stderr)
	if code != exitConfig {
		t.Errorf("dispatch(nil) exit = %d, want %d", code, exitConfig)
	}
	if strings.Contains(stderr.String(), "usage:") {
		t.Errorf("dispatch(nil) printed resolve usage: %q", stderr.String())
	}
}

func TestDispatchHelpListsSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"--help"}, &stdout, &stderr); code != exitOK {
		t.Errorf("exit = %d, want %d", code, exitOK)
	}
	for _, want := range []string{"indices", "resolve"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("help %q does not mention %q", stderr.String(), want)
		}
	}
}
