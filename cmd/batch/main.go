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
	skipTraced := fs.Bool("skip-traced", false, "skip companies whose latest scrape run left a non-empty browser-use trace")
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
	targets, err := store.ListScrapeTargets(ctx, database, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "batch: list targets: %v\n", err)
		return 1
	}
	if slugs := splitSlugs(*onlySlugs); len(slugs) > 0 {
		targets = filterTargets(targets, slugs)
	}
	if *fromSlug != "" {
		targets = filterFromSlug(targets, *fromSlug)
	}
	if len(targets) == 0 {
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
	logger.Info("sweep started",
		slog.Int("companies", len(targets)),
		slog.Bool("skip_ok", *skipOK),
		slog.Bool("skip_traced", *skipTraced),
		slog.String("from_slug", *fromSlug),
		slog.Int("stop_after_failures", *stopAfter),
	)

	fmt.Fprintf(stdout, "batch: %d companies, sequential (skip-ok=%t skip-traced=%t)\n",
		len(targets), *skipOK, *skipTraced)
	ok, failed, skipped, consecutive := 0, 0, 0, 0
	stoppedEarly := false
	for i, t := range targets {
		if reason := shouldSkip(t, *skipOK, *skipTraced, cfg.DataDir); reason != "" {
			skipped++
			fmt.Fprintf(stdout, "[%d/%d] company=%s skip (%s)\n", i+1, len(targets), t.Slug, reason)
			logger.Info("company skipped",
				slog.Int("index", i+1), slog.Int("of", len(targets)),
				slog.String("company", t.Slug), slog.String("reason", reason),
			)
			continue
		}
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
	fmt.Fprintf(stdout, "batch done: ok=%d failed=%d skipped=%d total=%d\n", ok, failed, skipped, len(targets))
	return 0
}

// shouldSkip returns a non-empty reason when the target should be skipped under the given options,
// or "" when it should be scraped. skipOK skips a company whose latest run already finished ok;
// skipTraced skips a company whose latest run left a non-empty browser-use trace (the reliable
// signal that browser-use actually ran, as opposed to a run that said "ok" while the agent died
// before its first step). The two conditions are independent and OR'd together.
func shouldSkip(t store.ScrapeTarget, skipOK, skipTraced bool, dataDir string) string {
	if skipOK && t.LastRunStatus != nil && *t.LastRunStatus == "ok" {
		return "last run ok"
	}
	if skipTraced && t.LastRunID != nil && traceHasEvents(dataDir, *t.LastRunID) {
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
