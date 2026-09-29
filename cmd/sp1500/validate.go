// validate.go: the `sp1500 validate` subcommand - the browser-use validation pass.
//
// This is where the plan's M3 containment lives. The tier is behind ENABLE_BROWSER_USE, it needs an
// explicit Ollama host before it will escalate, and the exit code has to keep three outcomes apart
// that all report "nothing confirmed": a run that considered no companies, a run that considered
// companies and confirmed none, and a run that errored. The package-level tests pin the validation
// logic; this file pins the wiring and that mapping.
//
// design: docs/sp1500-plan.md, section 7.5.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"jobsapp/internal/browseruse"
	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/runvalidate"
)

// validateFlags holds the parsed command line.
type validateFlags struct {
	onlySlugs     string
	limit         int
	concurrency   int
	dryRun        bool
	escalate      bool
	refresh       bool
	staleAfter    time.Duration
	pageTimeout   time.Duration
	workerTimeout time.Duration
	agentTimeout  time.Duration
	agentSteps    int
	maxBodyBytes  int64
	evidenceBytes int64
	progress      bool
	help          bool
}

// parseValidateFlags parses the validate subcommand's arguments.
//
// As with resolve, a given-but-empty --only-slugs is an error rather than a request for every
// company: validating all 634 stored URLs when the operator named three is a real cost, and the two
// intentions must not be spelled the same way.
func parseValidateFlags(args []string, stderr io.Writer) (validateFlags, error) {
	var f validateFlags
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&f.onlySlugs, "only-slugs", "", "comma-separated company slugs to validate")
	fs.IntVar(&f.limit, "limit", 0, "cap the number of companies processed (0 means no cap)")
	fs.IntVar(&f.concurrency, "concurrency", 1, "simultaneous browser workers")
	fs.BoolVar(&f.dryRun, "dry-run", false, "record attempts but move no company stamp")
	fs.BoolVar(&f.escalate, "escalate", false, "let a browser-use agent navigate when a page does not render")
	// Skipping is the default, not an opt-in: a sweep answers "is this URL still a careers page?" and
	// a company already carrying a verdict has an answer. Re-fetching it costs a real page load and
	// buys nothing, so a re-run after an interruption resumes instead of starting over.
	fs.BoolVar(&f.refresh, "refresh", false, "validate every stored URL again, including ones that already have a verdict")
	fs.DurationVar(&f.staleAfter, "stale-after", 0, "validate companies with no verdict or one older than this, instead of the default skip-validated rule")
	fs.DurationVar(&f.pageTimeout, "page-timeout", 45*time.Second, "per-page browser budget")
	fs.DurationVar(&f.workerTimeout, "worker-timeout", 90*time.Second, "whole worker process budget for one render")
	fs.DurationVar(&f.agentTimeout, "agent-timeout", 5*time.Minute, "whole worker process budget for one agent escalation")
	fs.IntVar(&f.agentSteps, "agent-max-steps", 8, "maximum navigation steps for one escalation")
	fs.Int64Var(&f.maxBodyBytes, "max-body-bytes", 1_500_000, "bound on the rendered body handed to the gate")
	fs.Int64Var(&f.evidenceBytes, "evidence-bytes", 524_288, "bytes of each accepted page to retain for re-judging under future rules (0 keeps none)")
	fs.BoolVar(&f.progress, "progress", false, "print one line per company to stderr as it is validated")
	fs.BoolVar(&f.help, "help", false, "print usage")

	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if wasSet(fs, "only-slugs") && len(f.slugs()) == 0 {
		return f, fmt.Errorf("--only-slugs was given but names no company; omit the flag to validate every stored URL")
	}
	if f.limit < 0 {
		return f, fmt.Errorf("--limit must not be negative, got %d", f.limit)
	}
	if f.concurrency <= 0 {
		return f, fmt.Errorf("--concurrency must be positive, got %d", f.concurrency)
	}
	// The two re-validation rules are alternatives, not a union: --refresh means "everything" and
	// --stale-after means "the older ones", so asking for both is a contradiction rather than a
	// narrower set.
	if f.refresh && f.staleAfter > 0 {
		return f, fmt.Errorf("--refresh and --stale-after contradict each other; --refresh validates everything")
	}
	if f.staleAfter < 0 {
		return f, fmt.Errorf("--stale-after must not be negative, got %s", f.staleAfter)
	}
	if f.pageTimeout <= 0 {
		return f, fmt.Errorf("--page-timeout must be positive, got %s", f.pageTimeout)
	}
	if f.workerTimeout <= 0 {
		return f, fmt.Errorf("--worker-timeout must be positive, got %s", f.workerTimeout)
	}
	if f.agentTimeout <= 0 {
		return f, fmt.Errorf("--agent-timeout must be positive, got %s", f.agentTimeout)
	}
	if f.agentSteps <= 0 {
		return f, fmt.Errorf("--agent-max-steps must be positive, got %d", f.agentSteps)
	}
	if f.maxBodyBytes <= 0 {
		return f, fmt.Errorf("--max-body-bytes must be positive, got %d", f.maxBodyBytes)
	}
	if f.evidenceBytes < 0 {
		return f, fmt.Errorf("--evidence-bytes must not be negative, got %d", f.evidenceBytes)
	}
	return f, nil
}

// slugs splits and cleans the --only-slugs value.
func (f validateFlags) slugs() []string {
	var out []string
	for _, s := range strings.Split(f.onlySlugs, ",") {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// runValidate executes the subcommand and returns the process exit code.
func runValidate(args []string, stdout, stderr io.Writer) int {
	flags, err := parseValidateFlags(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "status=error step=config err=%q\n", singleLine(err.Error()))
		return exitConfig
	}
	if flags.help {
		fmt.Fprintln(stderr, "usage: sp1500 validate [--only-slugs a,b] [--limit n] [--concurrency n] [--dry-run] [--escalate] [--refresh|--stale-after d] [--page-timeout d] [--worker-timeout d] [--agent-timeout d] [--agent-max-steps n] [--progress]")
		return exitOK
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "status=error step=config err=%q\n", singleLine(err.Error()))
		return exitConfig
	}

	// The feature flag is checked before the database is opened, so a run that is not enabled leaves
	// no trace at all - not even a scrape_runs row in 'running'.
	if !cfg.BrowserUseEnabled {
		fmt.Fprintf(stderr, "status=error step=config err=%q\n",
			"ENABLE_BROWSER_USE is not set to a true value; the browser-use tier is opt-in")
		return exitConfig
	}
	if flags.escalate && cfg.OllamaHost == "" {
		fmt.Fprintf(stderr, "status=error step=config err=%q\n",
			"OLLAMA_HOST must be set to use --escalate: the agent needs a model host, and guessing one would fail every escalation silently")
		return exitConfig
	}

	command := strings.Fields(cfg.BrowserUseCommand)
	if len(command) == 0 {
		fmt.Fprintf(stderr, "status=error step=config err=%q\n", "BROWSER_WORKER_COMMAND names no command")
		return exitConfig
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "status=error step=open err=%q\n", singleLine(err.Error()))
		return exitConfig
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	// The universe is every stored URL this invocation could consider, before the verdict filter.
	// Its size minus the selected size is the number of already-validated sites this run skipped, and
	// that number belongs on the summary line: "companies=593 skipped=41" says a resumed sweep did
	// what it was asked, while a bare "companies=593" alone leaves an operator wondering whether the
	// other 41 were forgotten or deliberately passed over.
	universe, err := runvalidate.LoadStoredCompanies(ctx, database, runvalidate.StoredFilter{OnlySlugs: flags.slugs()})
	if err != nil {
		fmt.Fprintf(stderr, "status=error step=load err=%q\n", singleLine(err.Error()))
		return exitFailure
	}
	filter := selectionFilter(flags, time.Now())
	companies, err := runvalidate.LoadStoredCompanies(ctx, database, filter)
	if err != nil {
		fmt.Fprintf(stderr, "status=error step=load err=%q\n", singleLine(err.Error()))
		return exitFailure
	}
	skipped := len(universe) - len(companies)

	// The worker's response carries the rendered body inside a JSON envelope, so the capture bound
	// has to exceed the body bound by enough for escaping and the surrounding object. Sizing it to
	// the body alone made a healthy 1.5MB page read as a protocol error.
	renderer := &browseruse.SubprocessWorker{
		Command:        command,
		Timeout:        flags.workerTimeout,
		Stderr:         stderr,
		MaxStdoutBytes: flags.maxBodyBytes*2 + (1 << 20),
	}

	runner := runvalidate.Runner{
		DB:       database,
		Renderer: renderer,
		Now:      time.Now,
	}
	if flags.escalate {
		runner.Agent = &browseruse.SubprocessWorker{
			Command: command,
			Timeout: flags.agentTimeout,
			Stderr:  stderr,
		}
	}

	summary, err := runner.Run(ctx, companies, runvalidate.Options{
		DryRun:               flags.dryRun,
		OnlySlugs:            flags.slugs(),
		Limit:                flags.limit,
		Concurrency:          flags.concurrency,
		Escalate:             flags.escalate,
		DataDir:              cfg.DataDir,
		MaxBodyBytes:         flags.maxBodyBytes,
		EvidenceBodyBytes:    flags.evidenceBytes,
		RenderTimeoutSeconds: flags.pageTimeout.Seconds(),
		AgentMaxSteps:        flags.agentSteps,
		Model:                cfg.BrowserUseModel,
		Progress:             progressWriter(flags.progress, stderr),
	})
	if err != nil {
		fmt.Fprintf(stderr, "run=%d status=error step=run err=%q exit=%d\n",
			summary.RunID, singleLine(err.Error()), exitFailure)
		return exitFailure
	}

	code := validateExitCode(summary)
	if code == exitOK || code == exitEmpty {
		fmt.Fprintln(stdout, formatValidateLine(summary, flags.dryRun, skipped))
		return code
	}

	fmt.Fprintf(stderr, "run=%d status=error step=run failed=%d err=%q exit=%d\n",
		summary.RunID, summary.Failed, singleLine(summary.FirstError), code)
	return code
}

// selectionFilter turns the flags into the loader's filter.
//
// Skipping already-validated companies is the default. --refresh drops that rule entirely and
// --stale-after replaces it with an age window; the two are mutually exclusive and rejected at parse
// time, so this switch cannot silently prefer one.
func selectionFilter(flags validateFlags, now time.Time) runvalidate.StoredFilter {
	filter := runvalidate.StoredFilter{OnlySlugs: flags.slugs()}
	switch {
	case flags.refresh:
		// Every stored URL, verdict or not.
	case flags.staleAfter > 0:
		filter.VerdictCutoff = runvalidate.VerdictCutoff(now, flags.staleAfter)
	default:
		filter.SkipValidated = true
	}
	return filter
}

// progressWriter returns the per-company reporter, or nil when progress is off.
//
// One line per company in the same key=value shape as every other line this tree prints, to stderr
// so the stdout success line stays exactly one line. A sweep of 600 URLs runs for a long time, and
// silence for an hour is indistinguishable from a hang.
func progressWriter(enabled bool, w io.Writer) func(runvalidate.Progress) {
	if !enabled {
		return nil
	}
	return func(p runvalidate.Progress) {
		if p.Err != nil {
			fmt.Fprintf(w, "company=%s category=failed status=%q err=%q\n",
				p.Slug, "error", singleLine(p.Err.Error()))
			return
		}
		reason := string(p.Reason)
		if reason == "" {
			reason = "-"
		}
		fmt.Fprintf(w, "company=%s category=%s reason=%s http=%d escalated=%d\n",
			p.Slug, p.Category, reason, p.HTTPStatus, boolToInt(p.Escalated))
	}
}

// validateExitCode maps a summary to the process exit code.
//
// The distinction this exists to keep: "considered companies and confirmed none" is exit 4, while
// "errored" is exit 1. Both report zero confirmed, and deriving the code from the confirmed count
// alone collapses them - the same failure the resolve command's mapping was written to prevent.
func validateExitCode(summary runvalidate.Summary) int {
	switch {
	case summary.Failed > 0:
		return exitFailure
	case summary.Confirmed > 0:
		return exitOK
	default:
		return exitEmpty
	}
}

// formatValidateLine renders the one success line.
//
// All three verdict classes plus the escalation count are on the line rather than only in the exit
// code, because the point of the run is the split: "confirmed 480, wrong 61, unverifiable 93" is
// the measurement, and a single accepted count cannot express it. skipped= is the fourth number
// that makes a resumed run legible.
func formatValidateLine(summary runvalidate.Summary, dryRun bool, skipped int) string {
	return fmt.Sprintf(
		"run_id=%d dry_run=%d companies=%d skipped=%d confirmed=%d wrong=%d unverifiable=%d escalated=%d failed=%d duration_ms=%d",
		summary.RunID, boolToInt(dryRun), summary.Found, skipped, summary.Confirmed, summary.Wrong,
		summary.Unverifiable, summary.Escalated, summary.Failed, summary.Duration.Milliseconds())
}
