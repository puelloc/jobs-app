// resolve.go: the `sp1500 resolve` subcommand.
//
// The command is a thin layer over internal/runresolve, but it is where per-company errors collapse
// into one exit code, and that is exactly where a bug can hide: a run that failed on every company
// and a run that found nothing both look like "zero resolves" from the outside. The summary's Failed
// and FirstError fields exist for that reason, and this file must read them rather than inferring
// the outcome from the resolved count alone.
//
// design: docs/sp1500-plan.md, section 7.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/httpfetch"
	"jobsapp/internal/runresolve"
)

// Exit codes, matching the documented scheme.
const (
	exitOK        = 0
	exitFailure   = 1 // network, parse or database error aborted the run
	exitConfig    = 2 // bad configuration or migration failure
	exitLock      = 3 // database write contention
	exitEmpty     = 4 // the input was non-empty and nothing was accepted, with no errors
	exitCollision = 5 // an evidence path collided within the same run
)

// resolveFlags holds the parsed command line.
type resolveFlags struct {
	onlySlugs     string
	limit         int
	refresh       bool
	refreshFailed bool
	concurrency   int
	dryRun        bool
	userAgent     string
	baseURL       string
	help          bool
}

// parseResolveFlags parses the resolve subcommand's arguments.
//
// It returns an error for values that are wrong rather than merely absent, so the caller can exit 2
// instead of silently running with a default. An empty --only-slugs is the case that matters most:
// treating it as "no restriction" would resolve all 1,500 companies when the operator asked for a
// named handful.
func parseResolveFlags(args []string, stderr io.Writer) (resolveFlags, error) {
	var f resolveFlags
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&f.onlySlugs, "only-slugs", "", "comma-separated company slugs to resolve")
	fs.IntVar(&f.limit, "limit", 0, "cap the number of companies processed (0 means no cap)")
	fs.BoolVar(&f.refresh, "refresh", false, "re-resolve companies that already have a career site URL")
	fs.BoolVar(&f.refreshFailed, "refresh-failed", false, "re-resolve only companies with no career site URL")
	fs.IntVar(&f.concurrency, "concurrency", 4, "simultaneous workers")
	fs.BoolVar(&f.dryRun, "dry-run", false, "record attempts but change no company")
	fs.StringVar(&f.userAgent, "user-agent", "", "override the configured User-Agent")
	fs.StringVar(&f.baseURL, "base-url", "", "override the API host (used by tests and mirrors)")
	fs.BoolVar(&f.help, "help", false, "print usage")

	if err := fs.Parse(args); err != nil {
		return f, err
	}

	// A given-but-empty value is a mistake, not a request for everything.
	//
	// The check is on the parsed slugs, not on the raw string: " , ," is not empty yet yields zero
	// slugs, and treating that as "no restriction" would resolve all 1,500 companies when the
	// operator asked for a named handful. Validating the raw value alone let exactly that through.
	if wasSet(fs, "only-slugs") && len(f.slugs()) == 0 {
		return f, fmt.Errorf("--only-slugs was given but names no company; omit the flag to resolve every company")
	}
	if f.limit < 0 {
		return f, fmt.Errorf("--limit must not be negative, got %d", f.limit)
	}
	if f.concurrency <= 0 {
		return f, fmt.Errorf("--concurrency must be positive, got %d", f.concurrency)
	}
	if f.refresh && f.refreshFailed {
		// The two rules select disjoint sets, so asking for both is a contradiction rather than a
		// union: --refresh means "including settled companies" and --refresh-failed means "only
		// unsettled ones".
		return f, fmt.Errorf("--refresh and --refresh-failed contradict each other; choose one")
	}
	return f, nil
}

// wasSet reports whether a flag appeared on the command line.
func wasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// slugs splits and cleans the --only-slugs value.
func (f resolveFlags) slugs() []string {
	var out []string
	for _, s := range strings.Split(f.onlySlugs, ",") {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// runResolve executes the subcommand and returns the process exit code.
//
// args are the arguments after the subcommand name. stdout receives the one success line; stderr
// receives failures. Both are parameters so the command is testable without touching the process's
// environment or its streams.
func runResolve(args []string, stdout, stderr io.Writer) int {
	flags, err := parseResolveFlags(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "status=error step=config err=%q\n", singleLine(err.Error()))
		return exitConfig
	}
	if flags.help {
		fmt.Fprintln(stderr, "usage: sp1500 resolve [--only-slugs a,b] [--limit n] [--refresh|--refresh-failed] [--dry-run] [--concurrency n] [--user-agent ua]")
		return exitOK
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "status=error step=config err=%q\n", singleLine(err.Error()))
		return exitConfig
	}
	if flags.userAgent != "" {
		cfg.UserAgent = flags.userAgent
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		// A migration failure leaves no run row: the process never reached the network.
		fmt.Fprintf(stderr, "status=error step=open err=%q\n", singleLine(err.Error()))
		return exitConfig
	}
	defer func() { _ = database.Close() }()

	fetcher, err := httpfetch.New(cfg.UserAgent)
	if err != nil {
		// An empty User-Agent is a configuration error, not a fallback to something browser-like.
		fmt.Fprintf(stderr, "status=error step=config err=%q\n", singleLine(err.Error()))
		return exitConfig
	}
	// The limiter is what makes this courteous rather than a flood: per-host concurrency of one and
	// a floor between requests to the same host. Building the fetcher without it would work and be
	// rude, so the wrapping happens here where it cannot be forgotten.
	limiter := httpfetch.NewLimiter(fetcher, httpfetch.WithConcurrency(flags.concurrency))

	ctx := context.Background()
	companies, err := runresolve.LoadCompanies(ctx, database, flags.slugs())
	if err != nil {
		fmt.Fprintf(stderr, "status=error step=load err=%q\n", singleLine(err.Error()))
		return exitFailure
	}

	// Tier 1 is enabled only when it can help: a company with no website needs it, and a company
	// that has one gains nothing from two API round trips.
	resolveHomepages := false
	for _, c := range companies {
		if c.Website == "" {
			resolveHomepages = true
			break
		}
	}

	runner := runresolve.Runner{
		DB:        database,
		Fetcher:   limiter,
		Wikipedia: flags.baseURL,
		Wikidata:  flags.baseURL,
		Now:       time.Now,
	}

	summary, err := runner.Run(ctx, companies, runresolve.Options{
		DryRun:           flags.dryRun,
		OnlySlugs:        flags.slugs(),
		Limit:            flags.limit,
		Refresh:          flags.refresh,
		RefreshFailed:    flags.refreshFailed,
		Concurrency:      flags.concurrency,
		DataDir:          cfg.DataDir,
		ResolveHomepages: resolveHomepages,
	})
	if err != nil {
		fmt.Fprintf(stderr, "run=%d status=error step=run err=%q exit=%d\n",
			summary.RunID, singleLine(err.Error()), exitFailure)
		return exitFailure
	}

	code := exitCodeFor(summary)

	// The summary line prints whenever the run itself completed, including the zero-result case:
	// exit 4 means the run did its job and found nothing, which is not a failure to report on
	// stderr.
	if code == exitOK || code == exitEmpty || code == exitCollision {
		fmt.Fprintln(stdout, formatSummaryLine(summary, flags.dryRun))
		return code
	}

	// A run where items errored reports on stderr, naming the first failure rather than only a count.
	fmt.Fprintf(stderr, "run=%d status=error step=run failed=%d err=%q exit=%d\n",
		summary.RunID, summary.Failed, singleLine(summary.FirstError), code)
	return code
}

// exitCodeFor maps a summary to the process exit code.
//
// The distinction this function exists to make: "considered companies and accepted nothing" is exit
// 4, while "errored on every company" is exit 1. Both report zero accepts, so deriving the code from
// the accepted count alone collapses them - which is precisely how a bug that broke every write
// looks like a quiet day.
func exitCodeFor(summary runresolve.Summary) int {
	switch {
	case summary.Failed > 0:
		// An error is an error even when other companies succeeded: a run that accepted 1,203 and
		// errored on 295 must not exit 0 as though it were clean.
		return exitFailure
	case summary.Resolved > 0:
		return exitOK
	default:
		// Nothing resolved and nothing errored: a genuine zero-result run.
		return exitEmpty
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// formatSummaryLine renders the one success line.
//
// failed= is part of the line rather than only of the exit code, because a run that produced zero
// accepts due to a bug should say failed=1498 rather than leaving an operator to read resolved=0 as
// a quiet result. It is built here rather than inline so the shape can be asserted directly.
func formatSummaryLine(summary runresolve.Summary, dryRun bool) string {
	return fmt.Sprintf(
		"run_id=%d dry_run=%d companies=%d resolved=%d unresolved=%d skipped=%d failed=%d duration_ms=%d",
		summary.RunID, boolToInt(dryRun), summary.Found, summary.Resolved, summary.Unresolved,
		summary.Skipped, summary.Failed, summary.Duration.Milliseconds())
}
