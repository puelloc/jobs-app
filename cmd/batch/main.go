// Command batch runs the listings scrape over every eligible company, one at a time, and is the
// concurrency boundary for a full sweep: it never runs two scrapes at once, so SQLite sees one
// writer and the model host sees one browser agent. It shells to cmd/scrape per company and reaps
// each process before starting the next, pausing between companies for politeness.
//
// It does not own scrape_runs rows; each cmd/scrape it launches starts and finishes its own, so the
// per-company status and traces the UI shows are unchanged.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/exitcode"
	"jobsapp/internal/logging"
	"jobsapp/internal/store"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("batch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 0, "cap the number of companies (0 means all)")
	onlySlugs := fs.String("only-slugs", "", "comma-separated company slugs to scrape")
	fromSlug := fs.String("from-slug", "", "start from this slug (alphabetical) and skip earlier companies")
	stopAfter := fs.Int("stop-after-failures", 0, "stop after this many consecutive company failures (0 = never)")
	skipOK := fs.Bool("skip-ok", false, "skip companies whose latest scrape run already finished ok")
	skipTraced := fs.Bool("skip-traced", false,
		"skip companies whose latest SUCCESSFUL scrape run left a browser-use trace (a failed or interrupted run leaves a partial trace, and those are the ones worth retrying)")
	delay := fs.Duration("delay", 5*time.Second, "pause between companies")
	scrapeCmd := fs.String("scrape-cmd", "", "command to run per company (default: config SCRAPE_COMMAND)")
	sweepRunID := fs.Int64("run-id", 0,
		"scrape_runs id of this sweep (0 = unknown); recorded on every log line as sweep_id so the sweep's logs join its database row")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "batch: config: %v\n", err)
		return 1
	}
	cmdWords := strings.Fields(*scrapeCmd)
	if len(cmdWords) == 0 {
		cmdWords = strings.Fields(cfg.ScrapeCommand)
	}
	if len(cmdWords) == 0 {
		fmt.Fprintln(stderr, "batch: -scrape-cmd or SCRAPE_COMMAND must name the scrape binary")
		return 1
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "batch: open database %s: %v\n", cfg.DBPath, err)
		return 1
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()
	// Every candidate, uncapped. The cap has to apply to the companies that will actually be scraped,
	// not to the pool they are drawn from (see planSweep).
	candidates, err := store.ListScrapeTargets(ctx, database, 0)
	if err != nil {
		fmt.Fprintf(stderr, "batch: list targets: %v\n", err)
		return 1
	}
	if len(candidates) == 0 {
		fmt.Fprintln(stdout, "batch: no eligible companies (need a career URL and a classified vendor)")
		return 0
	}

	// One trace for the whole sweep, inherited from whoever launched us (the server passes the id of
	// the scrape_runs row it opened) or generated here for a hand-run sweep. Every company's child
	// process inherits it, so "everything that happened during this sweep" is one query even though
	// the children write their own logs.
	sweepIdentity := logging.IdentityFromEnv()
	if *sweepRunID != 0 {
		sweepIdentity.SweepID = sweepRunID
	}
	logger := logging.New(
		logging.Config{
			App:     logging.App(),
			Service: logging.Service("batch"),
			Level:   os.Getenv(logging.EnvLevel),
			Writer:  stderr,
		},
		sweepIdentity,
	)
	childEnv := append(os.Environ(), logger.Identity().Env()...)

	targets, skipNotes, considered := planSweep(
		candidates,
		splitSlugs(*onlySlugs),
		*fromSlug,
		*limit,
		func(t store.ScrapeTarget) string { return shouldSkip(t, *skipOK, *skipTraced, cfg.DataDir) },
	)
	for _, note := range skipNotes {
		fmt.Fprintf(stdout, "[%d/%d] company=%s skip (%s)\n", note.Index+1, considered, note.Slug, note.Reason)
		logger.Info("company skipped",
			slog.Int("index", note.Index+1), slog.Int("of", considered),
			slog.String("company", note.Slug), slog.String("reason", note.Reason),
		)
	}
	if len(targets) == 0 {
		// Two different empties, and only one of them means "you asked for nothing".
		if len(skipNotes) > 0 {
			fmt.Fprintf(stdout,
				"batch: nothing to do: all %d candidate(s) matched a skip rule (skip-ok=%t skip-traced=%t)\n",
				len(skipNotes), *skipOK, *skipTraced)
			logger.Info("nothing to do",
				slog.Int("skipped", len(skipNotes)), slog.Int("considered", considered))
		} else {
			fmt.Fprintln(stdout, "batch: no companies matched -only-slugs/-from-slug")
			logger.Info("nothing to do", slog.Int("considered", considered), slog.String("reason", "filters"))
		}
		return 0
	}

	logger.Info("sweep started",
		slog.Int("companies", len(targets)),
		slog.Int("candidates", considered),
		slog.Int("skipped", len(skipNotes)),
		slog.Bool("skip_ok", *skipOK),
		slog.Bool("skip_traced", *skipTraced),
		slog.String("from_slug", *fromSlug),
		slog.Int("limit", *limit),
		slog.Int("stop_after_failures", *stopAfter),
	)

	fmt.Fprintf(stdout, "batch: %d companies, sequential (skip-ok=%t skip-traced=%t)\n",
		len(targets), *skipOK, *skipTraced)
	ok, failed, skipped, consecutive := 0, 0, len(skipNotes), 0
	stoppedEarly := false
	for i, t := range targets {
		if i > 0 && *delay > 0 {
			time.Sleep(*delay)
		}
		// Persist the resume point before attempting: if the sweep is stopped (SIGTERM) or crashes
		// mid-company, the file already names the company a resume should start from.
		writeResumePoint(cfg.DataDir, t.Slug)
		fmt.Fprintf(stdout, "[%d/%d] company=%s vendor=%s\n", i+1, len(targets), t.Slug, t.Vendor)
		logger.Info("company started",
			slog.Int("index", i+1), slog.Int("of", len(targets)),
			slog.String("company", t.Slug), slog.String("vendor", t.Vendor),
		)

		argv := append([]string{}, cmdWords[1:]...)
		argv = append(argv, "--slug", t.Slug, "--vendor", t.Vendor)
		cmd := exec.CommandContext(ctx, cmdWords[0], argv...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		// The child inherits the sweep's trace, so its lines join this sweep rather than arriving as an
		// unrelated stream.
		cmd.Env = childEnv
		// The per-company duration is the number an optimisation question turns on, and it is not
		// recoverable afterwards from the child's own log (which is empty: it shares our stdout).
		companyStarted := time.Now()
		if err := cmd.Run(); err != nil {
			logger.Error("company failed",
				slog.String("company", t.Slug),
				slog.Int("index", i+1),
				slog.Int64("duration_ms", time.Since(companyStarted).Milliseconds()),
				slog.String("error", err.Error()),
			)
			fmt.Fprintf(stderr, "batch: company=%s failed: %v\n", t.Slug, err)
			failed++
			consecutive++
			if isOllamaUnreachable(err) {
				// Ollama is down: every remaining company would fail the same way, so stop rather
				// than churn. The resume point written above lets the UI resume from this company.
				fmt.Fprintf(stdout, "batch: stopped: model host unreachable (company=%s); resume with -from-slug %s\n",
					t.Slug, t.Slug)
				stoppedEarly = true
				break
			}
			if *stopAfter > 0 && consecutive >= *stopAfter {
				fmt.Fprintf(stdout, "batch: stopped after %d consecutive failures (last company=%s); resume with -from-slug %s\n",
					consecutive, t.Slug, t.Slug)
				stoppedEarly = true
				break
			}
			continue
		}
		ok++
		consecutive = 0
		logger.Info("company finished",
			slog.String("company", t.Slug),
			slog.Int("index", i+1),
			slog.Int64("duration_ms", time.Since(companyStarted).Milliseconds()),
		)
	}

	// A sweep that ran every company to the end has nothing to resume: clear the resume point so the
	// UI does not offer a stale "Resume" button. A stopped sweep keeps it for one-click resume.
	if !stoppedEarly {
		clearResumePoint(cfg.DataDir)
	}

	logger.Info("sweep finished",
		slog.Int("ok", ok),
		slog.Int("failed", failed),
		slog.Int("skipped", skipped),
		slog.Int("total", len(targets)),
		slog.Bool("stopped_early", stoppedEarly),
	)
	// considered, not len(targets): the tally should add up (planned + skipped), so "total" means every
	// company this sweep looked at rather than only the ones it ran.
	fmt.Fprintf(stdout, "batch done: ok=%d failed=%d skipped=%d total=%d\n", ok, failed, skipped, considered)
	return 0
}

// skipNote is a company this sweep decided not to visit, and why.
type skipNote struct {
	// Index is the company's position in the filtered candidate list, so a skip line points at the same
	// list the run was drawn from.
	Index  int
	Slug   string
	Reason string
}

// planSweep decides which companies a sweep will actually visit, and which it will not.
//
// The ORDER is the whole point, and getting it wrong is subtle enough to have shipped once:
//
//  1. -only-slugs is an exact selection, so it is applied first. A cap applied before it would take the
//     first N companies alphabetically and then filter, which can drop the slug that was asked for while
//     the quota is already spent.
//  2. -from-slug is positional within what remains.
//  3. Companies that need no work (already done, already traced) are dropped.
//  4. -limit is applied LAST, to the list that will really be run.
//
// Applying the cap at step 1 - which is what the caller used to do - meant a request for 5 companies
// with "skip companies with a trace" could scrape nothing at all: five already-traced candidates
// consumed the entire quota, and the sweep reported a successful zero.
func planSweep(
	candidates []store.ScrapeTarget,
	onlySlugs []string,
	fromSlug string,
	limit int,
	skip func(store.ScrapeTarget) string,
) (planned []store.ScrapeTarget, skipped []skipNote, considered int) {
	filtered := candidates
	if len(onlySlugs) > 0 {
		filtered = filterTargets(filtered, onlySlugs)
	}
	if fromSlug != "" {
		filtered = filterFromSlug(filtered, fromSlug)
	}
	considered = len(filtered)

	planned = make([]store.ScrapeTarget, 0, considered)
	for i, target := range filtered {
		// The cap is tested before the skip rule, not after: that rule stats a trace file per company,
		// and there is no sense deciding about companies the quota has already excluded.
		if limit > 0 && len(planned) >= limit {
			break
		}
		if reason := skip(target); reason != "" {
			skipped = append(skipped, skipNote{Index: i, Slug: target.Slug, Reason: reason})
			continue
		}
		planned = append(planned, target)
	}
	return planned, skipped, considered
}

// shouldSkip returns a non-empty reason when the target should be skipped under the given options,
// or "" when it should be scraped. skipOK skips a company whose latest run already finished ok;
// skipTraced skips a company whose latest run left a non-empty browser-use trace (the reliable
// signal that browser-use actually ran, as opposed to a run that said "ok" while the agent died
// before its first step). The two conditions are independent and OR'd together.
func shouldSkip(t store.ScrapeTarget, skipOK, skipTraced bool, dataDir string) string {
	finishedOK := t.LastRunStatus != nil && *t.LastRunStatus == "ok"
	if skipOK && finishedOK {
		return "last run ok"
	}
	// A trace only means "this company is done" when the run that left it succeeded.
	//
	// A failed or interrupted run leaves a partial trace too, and skipping on that alone skips exactly
	// the companies worth retrying. abbott-laboratories is the worked example: it times out on every
	// sweep, leaves a few steps of trace, and was therefore skipped forever by this rule - the one
	// company that most needed another attempt. It also skips the company a resume point names, because
	// the sweep that died mid-company left it with a partial trace and an `interrupted by restart`
	// status, so resuming would silently step past the company being resumed from.
	if skipTraced && finishedOK && t.LastRunID != nil && traceHasEvents(dataDir, *t.LastRunID) {
		return "browser-use trace present"
	}
	return ""
}

// traceHasEvents reports whether a run's trace file exists and contains at least one event line. An
// agent that died before its first step leaves either no file or an empty file (TraceWriter opens the
// file eagerly), so a non-empty file is exactly the "browser-use actually navigated" signal.
func traceHasEvents(dataDir string, runID int64) bool {
	raw, err := os.ReadFile(filepath.Join(dataDir, "traces", fmt.Sprintf("%d.jsonl", runID)))
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(raw))) > 0
}

// resumeFilePath is where the sweep leaves its resume point: the slug of the company a resume should
// start from. The API serves it (GET /api/sweep/position) so the UI can offer a one-click resume.
func resumeFilePath(dataDir string) string {
	return filepath.Join(dataDir, "sweep", "resume")
}

// writeResumePoint records the slug currently being attempted. It is written before the attempt, not
// after, so a stop (SIGTERM) or crash mid-company still leaves a correct resume point behind.
func writeResumePoint(dataDir, slug string) {
	p := resumeFilePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(slug), 0o644)
}

// clearResumePoint removes the resume point once a sweep has run to completion.
func clearResumePoint(dataDir string) {
	_ = os.Remove(resumeFilePath(dataDir))
}

// isOllamaUnreachable reports whether a child scrape failed with exitcode.OllamaUnreachable, which
// cmd/scrape returns when the model host could not be reached after its retries.
func isOllamaUnreachable(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == exitcode.OllamaUnreachable
}

// splitSlugs splits a comma-separated slug list, dropping blanks.
func splitSlugs(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// filterTargets keeps only the targets whose slug was requested, preserving input order.
func filterTargets(targets []store.ScrapeTarget, slugs []string) []store.ScrapeTarget {
	want := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		want[s] = true
	}
	out := make([]store.ScrapeTarget, 0, len(slugs))
	for _, t := range targets {
		if want[t.Slug] {
			out = append(out, t)
		}
	}
	return out
}

// filterFromSlug keeps companies whose slug sorts at or after fromSlug (case-insensitive), so a
// sweep can resume from where it left off. The list is ordered by name, which tracks the slug for
// these companies closely enough for resume-by-point.
func filterFromSlug(targets []store.ScrapeTarget, fromSlug string) []store.ScrapeTarget {
	from := strings.ToLower(strings.TrimSpace(fromSlug))
	out := make([]store.ScrapeTarget, 0, len(targets))
	for _, t := range targets {
		if strings.ToLower(t.Slug) >= from {
			out = append(out, t)
		}
	}
	return out
}
