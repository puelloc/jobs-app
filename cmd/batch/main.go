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
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
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

	fmt.Fprintf(stdout, "batch: %d companies, sequential (skip-ok=%t skip-traced=%t)\n",
		len(targets), *skipOK, *skipTraced)
	ok, failed, skipped, consecutive := 0, 0, 0, 0
	for i, t := range targets {
		if reason := shouldSkip(t, *skipOK, *skipTraced, cfg.DataDir); reason != "" {
			skipped++
			fmt.Fprintf(stdout, "[%d/%d] company=%s skip (%s)\n", i+1, len(targets), t.Slug, reason)
			continue
		}
		if i > 0 && *delay > 0 {
			time.Sleep(*delay)
		}
		fmt.Fprintf(stdout, "[%d/%d] company=%s vendor=%s\n", i+1, len(targets), t.Slug, t.Vendor)

		argv := append([]string{}, cmdWords[1:]...)
		argv = append(argv, "--slug", t.Slug, "--vendor", t.Vendor)
		cmd := exec.CommandContext(ctx, cmdWords[0], argv...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(stderr, "batch: company=%s failed: %v\n", t.Slug, err)
			failed++
			consecutive++
			if *stopAfter > 0 && consecutive >= *stopAfter {
				fmt.Fprintf(stdout, "batch: stopped after %d consecutive failures (last company=%s); resume with -from-slug %s\n",
					consecutive, t.Slug, t.Slug)
				break
			}
			continue
		}
		ok++
		consecutive = 0
	}

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
