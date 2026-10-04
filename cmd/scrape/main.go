// Command scrape runs the full remote-US listings scrape for one company: a browser-use agent finds
// the filtered listings URL, a render pass extracts the postings, and the store writes the US ones.
// It is the "job" the trigger endpoint launches, and it owns its scrape_runs row (start → finish).
//
// It shells out to the two Python tools (worker/remote_roles_probe.py and worker/listings_fetch.py),
// whose stdout is one JSON object each, and is run from the repository root so their relative paths
// resolve.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/exitcode"
	"jobsapp/internal/jobposting"
	"jobsapp/internal/logging"
	"jobsapp/internal/robots"
	"jobsapp/internal/store"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type probeAnswer struct {
	ListingsURL             string `json:"listings_url"`
	HasRemoteSoftwareRoles  bool   `json:"has_remote_software_roles"`
	RemoteSoftwareRoleCount int    `json:"remote_software_role_count"`
	Evidence                string `json:"evidence"`
	// Blocked separates "this board has no openings" from "this board would not show us anything".
	// The two look identical from the outside and need opposite responses.
	Blocked     bool   `json:"blocked"`
	BlockReason string `json:"block_reason"`
}

type probeOutput struct {
	OK      bool         `json:"ok"`
	Timeout bool         `json:"timeout"`
	Error   string       `json:"error"`
	Answer  *probeAnswer `json:"answer"`
	// The process, not just the answer. A company can be resolved correctly by a run that took 19 steps
	// and went somewhere it should not have, and without these the only thing recorded is that it
	// worked - which is exactly the outcome that teaches nothing.
	Steps      int             `json:"steps"`
	ElapsedSec float64         `json:"elapsed_sec"`
	Successful bool            `json:"is_successful"`
	Judged     bool            `json:"judged"`
	Judgement  json.RawMessage `json:"judgement"`
}

// judgementNote reduces the judge's report to a short, loggable string. It is a trajectory-consistency
// check, not a fact check: it can flag a right answer reached by an unproven path, which is precisely
// the signal worth keeping.
func judgementNote(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return firstChars(string(raw), 200)
	}
	for _, key := range []string{"verdict", "reasoning", "failure_reason", "reason", "error"} {
		if value, ok := fields[key]; ok {
			if text, ok := value.(string); ok && text != "" {
				return firstChars(text, 200)
			}
		}
	}
	return firstChars(string(raw), 200)
}

// qualityNote names the shapes that resolved without really working.
//
// Every one of these is a run that reports success or a clean skip while something upstream is wrong,
// which is why they are worth a field of their own: they are invisible in the outcome and they are the
// difference between a pipeline that works and one that merely produces no errors.
func qualityNote(resolution string, remoteConfirmed bool, found, inserted, skippedNonUS int) string {
	switch {
	case resolution == "cache" && found == 0:
		// The cached URL was reused and yielded nothing at all. Either the board is genuinely empty or
		// the cached URL has gone stale - and the TTL is the thing to reconsider.
		return "cached_url_yielded_nothing"
	case remoteConfirmed && found == 0:
		return "agent_confirmed_remote_but_fetch_found_nothing"
	case remoteConfirmed && found > 0 && inserted == 0 && skippedNonUS > 0:
		return "agent_confirmed_remote_but_all_postings_non_us"
	case remoteConfirmed && found > 0 && inserted == 0 && skippedNonUS == 0:
		return "agent_confirmed_remote_but_nothing_stored"
	default:
		return ""
	}
}

// runForwardingStderr runs cmd and returns its stdout, while letting its stderr reach the given writer.
//
// It exists because exec.Cmd.Output() with a nil Stderr captures the child's stderr into a 32KB buffer
// and throws it away when the command succeeds. That silently swallowed everything the worker writes to
// stderr: the per-step JSON emitted for the log store, and every Python traceback. A worker that died
// inside its own code looked exactly like one that said nothing.
func runForwardingStderr(cmd *exec.Cmd, stderr io.Writer) ([]byte, error) {
	cmd.Stderr = stderr
	return cmd.Output()
}

// round1 trims a duration to one decimal, which is all the precision a log line needs.
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// orSeconds prefers the worker's own measurement of its phase, falling back to the wall clock the
// parent saw. The worker's number excludes process startup and is the more honest one.
func orSeconds(reported float64, started time.Time) float64 {
	if reported > 0 {
		return reported
	}
	return time.Since(started).Seconds()
}

// firstChars bounds a free-text field so one verbose worker cannot dominate the log line.
func firstChars(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

type fetchJob struct {
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	RawData      string  `json:"raw_data"`
	Error        string  `json:"error"`
	BlockReason  string  `json:"block_reason"`
	Country      *string `json:"country"`
	LocationText *string `json:"location_text"`
	IsUS         *bool   `json:"is_us"`
}

type fetchOutput struct {
	Jobs       []fetchJob `json:"jobs"`
	TotalLinks int        `json:"total_links"`
	ElapsedSec float64    `json:"elapsed_sec"`
	// A listings page that challenges or blocks us renders successfully and yields nothing, which is
	// indistinguishable from an empty board without this.
	Blocked     bool   `json:"blocked"`
	BlockReason string `json:"block_reason"`
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scrape", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slug := fs.String("slug", "", "company slug (required)")
	vendor := fs.String("vendor", "", "vendor name: eightfold, phenom, ... (required)")
	maxJobs := fs.Int("max-jobs", 25, "postings to extract")
	maxSteps := fs.Int("max-steps", 25, "agent navigation budget")
	timeout := fs.Duration("timeout", 20*time.Minute, "whole-scrape budget")
	minCrawlDelay := fs.Duration("min-crawl-delay", 0, "floor on the per-host delay between fetches")
	runIDFlag := fs.Int64("run-id", 0, "existing scrape_runs id to finish (0 starts a new one)")
	listingsURLTTL := fs.Duration("listings-url-ttl", 7*24*time.Hour,
		"reuse a cached filtered listings URL for this long before re-resolving it with the agent (0 = always re-resolve)")
	refreshListingsURL := fs.Bool("refresh-listings-url", false,
		"ignore the cached listings URL and re-resolve it with the agent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *slug == "" || *vendor == "" {
		fmt.Fprintln(stderr, "scrape: -slug and -vendor are required")
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "scrape: config: %v\n", err)
		return 1
	}
	if cfg.OllamaHost == "" {
		fmt.Fprintln(stderr, "scrape: OLLAMA_HOST must be set: the agent needs a model host")
		return 1
	}
	workerCmd := strings.Fields(cfg.BrowserUseCommand)
	if len(workerCmd) == 0 {
		fmt.Fprintln(stderr, "scrape: BROWSER_WORKER_COMMAND names no interpreter")
		return 1
	}
	python := workerCmd[0]

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "scrape: open database: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	company, err := store.ScrapeCompanyBySlug(ctx, database, *slug)
	if err != nil {
		fmt.Fprintf(stderr, "scrape: company %q: %v\n", *slug, err)
		return 1
	}
	if company.CareerSiteURL == "" {
		fmt.Fprintf(stderr, "scrape: company %q has no stored career_site_url\n", *slug)
		return 1
	}
	jobPlatformID, err := store.PlatformIDByName(ctx, database, *vendor)
	if err != nil {
		fmt.Fprintf(stderr, "scrape: vendor %q: %v\n", *vendor, err)
		return 1
	}
	runPlatformID, err := store.PlatformIDByName(ctx, database, "career_listings")
	if err != nil {
		fmt.Fprintf(stderr, "scrape: career_listings platform: %v\n", err)
		return 1
	}

	runID := *runIDFlag
	if runID == 0 {
		// Record which company this run is for: the batch sweep needs it to decide whether a
		// company already succeeded / already left a trace, so a re-run can skip it.
		runID, _, err = store.StartCompanyRun(ctx, database, runPlatformID, company.ID)
		if err != nil {
			fmt.Fprintf(stderr, "scrape: start run: %v\n", err)
			return 1
		}
	}
	finish := func(status string, found, inserted, updated int64, errText string) {
		_ = store.FinishRun(ctx, database, runID, status, found, inserted, updated, strPtr(errText))
	}

	// The structured log carries this run's correlation on every line: the trace inherited from the
	// sweep (or a fresh one when run by hand), the sweep and run ids, and the company. That is what
	// makes a company's lines attributable even though its own log file is empty during a sweep,
	// because the child inherits the batch's stdout.
	logger := logging.New(
		logging.Config{
			App:     logging.App(),
			Service: logging.Service("scrape"),
			Level:   os.Getenv(logging.EnvLevel),
			Writer:  stderr,
		},
		logging.IdentityFromEnv().With(&runID, company.Slug),
	)
	// The Python children inherit the correlation, so anything they log joins the same trace.
	// Children log under this service, so a company's whole story - including every agent step the
	// worker emits to stderr - is one query rather than several.
	childEnv := append(os.Environ(), logger.Identity().Env()...)
	childEnv = append(childEnv, logging.EnvService+"=scrape")
	logger.Info("start",
		slog.String("vendor", *vendor),
		slog.String("career_site_url", company.CareerSiteURL),
		slog.String("data_dir", cfg.DataDir),
	)

	trace := fmt.Sprintf("%s/traces/%d.jsonl", cfg.DataDir, runID)
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	// A reachability probe wants a shorter budget than the whole-scrape HTTP timeout: three retries
	// at 30s each would otherwise hold the run open for over a minute before giving up.
	probeClient := &http.Client{Timeout: 10 * time.Second}

	// classifyAgentFailure decides the exit code for a failed agent phase by re-checking whether the
	// model host is reachable. A down host is the one failure the batch runner must stop the whole
	// sweep on (every remaining company would fail the same way), so it is returned as
	// exitcode.OllamaUnreachable; anything else is an ordinary per-company failure.
	classifyAgentFailure := func(errText string) int {
		if reachErr := ollamaReachable(ctx, probeClient, cfg.OllamaHost); reachErr != nil {
			finish("error", 0, 0, 0, reachErr.Error())
			fmt.Fprintf(stderr, "scrape: %v\n", reachErr)
			return exitcode.OllamaUnreachable
		}
		finish("error", 0, 0, 0, errText)
		return 1
	}

	// 1. Resolve the filtered listings URL. The agent that finds it is the slow part of a scrape - a
	// local-model navigation loop - and its answer is stable for a company from sweep to sweep, so a
	// recently resolved URL is reused verbatim and the agent is skipped entirely. It runs only on a
	// cold cache, past the TTL, or when the caller forces a re-resolve.
	listingsURL, resolvedFrom := "", "agent"
	// blocked/blockReason record a challenge or refusal rather than a finding. Carried out of the agent
	// phase so the cache decision and the summary can both see it.
	blocked, blockReason := false, ""
	// Where the time went, per phase. Without this a scrape is one opaque number, and "the agent is
	// slow" and "the fetch is slow" call for opposite fixes.
	scrapeStarted := time.Now()
	agentSec, fetchSec, storeSec := 0.0, 0.0, 0.0
	agentSteps, agentJudged := 0, false
	judgement := ""
	// remoteConfirmed is the verdict the resolving agent reached. It is cached alongside the URL,
	// because a re-sweep that reads only the URL cannot tell a remote-filtered search page from a
	// company's unfiltered job board - and would store on-site roles as remote.
	remoteConfirmed := false

	if cached, ok := reuseCachedListingsURL(company, time.Now().UTC(), *listingsURLTTL, *refreshListingsURL); ok {
		listingsURL, resolvedFrom = cached, "cache"
		// A row cached before the verdict was recorded reads as unconfirmed, which is the conservative
		// reading: it makes the run skip the fetch rather than trust an unverified URL.
		remoteConfirmed = company.ListingsURLRemoteConfirmed != nil && *company.ListingsURLRemoteConfirmed
		// The cached path never runs the agent, so this run would otherwise have no trace at all -
		// and the run page, the -skip-traced sweep option, and the job page's "why did this match"
		// section all read one. Record the resolution the agent would have produced.
		appendTraceEvent(trace, map[string]any{
			"event":            "resolution",
			"source":           "cache",
			"listings_url":     cached,
			"resolved_at":      company.ListingsURLResolvedAt,
			"remote_confirmed": remoteConfirmed,
		})
		fmt.Fprintf(stdout, "run_id=%d company=%s listings_url=cache resolved_at=%s remote_confirmed=%t url=%s\n",
			runID, company.Slug, company.ListingsURLResolvedAt, remoteConfirmed, cached)
	}

	// The careers-page crawl delay only exists on the agent path; the cached path never touches the
	// careers page, so it has nothing to contribute here.
	var careersCrawlDelay time.Duration

	if listingsURL == "" {
		// 0. Politeness: honor the careers origin's robots.txt before the agent touches it.
		careersPolicy, err := robots.Fetch(ctx, httpClient, company.CareerSiteURL, cfg.UserAgent)
		if err != nil {
			finish("error", 0, 0, 0, fmt.Sprintf("robots: %v", err))
			fmt.Fprintf(stderr, "scrape: robots %s: %v\n", company.CareerSiteURL, err)
			return 1
		}
		if !careersPolicy.Allowed(company.CareerSiteURL, cfg.UserAgent) {
			finish("ok", 0, 0, 0, "")
			fmt.Fprintf(stdout, "run_id=%d company=%s robots=disallowed url=%s\n",
				runID, company.Slug, company.CareerSiteURL)
			return 0
		}
		careersCrawlDelay = careersPolicy.CrawlDelay(cfg.UserAgent)

		// Pre-flight the model host first so a down Ollama fails this company fast instead of
		// launching Chromium and then hanging on the first LLM call.
		if err := ollamaReachable(ctx, probeClient, cfg.OllamaHost); err != nil {
			finish("error", 0, 0, 0, err.Error())
			fmt.Fprintf(stderr, "scrape: %v\n", err)
			return exitcode.OllamaUnreachable
		}

		probeCmd := exec.CommandContext(ctx, python, "worker/remote_roles_probe.py",
			"--url", company.CareerSiteURL,
			"--company", company.Name,
			"--host", cfg.OllamaHost,
			"--model", cfg.BrowserUseModel,
			"--max-steps", fmt.Sprintf("%d", *maxSteps),
			"--trace", trace,
		)
		agentStarted := time.Now()
		probeCmd.Env = childEnv
		probeOut, err := runForwardingStderr(probeCmd, stderr)
		if err != nil {
			// The worker process failed. Re-check the host: if Ollama is down now, that is the cause
			// and the batch runner should stop; otherwise it is an ordinary agent failure.
			fmt.Fprintf(stderr, "scrape: agent: %v\n", err)
			logger.Error("agent failed",
				slog.String("span", "agent"),
				slog.String("error", err.Error()),
				slog.Bool("timeout", ctx.Err() != nil),
				slog.Float64("agent_s", round1(time.Since(agentStarted).Seconds())),
			)
			return classifyAgentFailure(fmt.Sprintf("agent: %v", err))
		}
		var probe probeOutput
		if err := json.Unmarshal(probeOut, &probe); err != nil {
			finish("error", 0, 0, 0, fmt.Sprintf("parse agent output: %v", err))
			fmt.Fprintf(stderr, "scrape: parse agent output: %v\n", err)
			return 1
		}
		if !probe.OK {
			// The agent itself failed (exception or timeout). Keep the reason: log it and mark the run
			// as error rather than silently folding it into "0 found".
			errText := probe.Error
			if errText == "" && probe.Timeout {
				errText = "agent timed out"
			}
			fmt.Fprintf(stdout, "run_id=%d company=%s agent_failed=1 error=%q timeout=%t\n",
				runID, company.Slug, probe.Error, probe.Timeout)
			// steps and elapsed are reported here too: a timeout that reached step 14 nearly worked,
			// and one that never left step 1 never started. "agent timed out" cannot tell them apart.
			logger.Error("agent failed",
				slog.String("error", probe.Error),
				slog.Bool("timeout", probe.Timeout),
				slog.String("span", "agent"),
				slog.Int("agent_steps", probe.Steps),
				slog.Float64("agent_s", round1(orSeconds(probe.ElapsedSec, agentStarted))),
			)
			return classifyAgentFailure(errText)
		}
		if probe.Answer == nil || probe.Answer.ListingsURL == "" {
			finish("ok", 0, 0, 0, "")
			evidence := ""
			if probe.Answer != nil {
				evidence = probe.Answer.Evidence
			}
			fmt.Fprintf(stdout, "run_id=%d company=%s listings=none found=0 evidence=%q\n",
				runID, company.Slug, evidence)
			logger.Info("skip",
				slog.String("reason", "listings_none"),
				slog.String("span", "agent"),
				slog.String("evidence", evidence),
				slog.Int("agent_steps", probe.Steps),
				slog.Float64("agent_s", round1(orSeconds(probe.ElapsedSec, agentStarted))),
			)
			return 0
		}

		// Log what the agent decided: its evidence and the count it saw, so a later zero is diagnosable.
		fmt.Fprintf(stdout, "run_id=%d company=%s agent: has_remote=%t count=%d evidence=%q\n",
			runID, company.Slug, probe.Answer.HasRemoteSoftwareRoles,
			probe.Answer.RemoteSoftwareRoleCount, probe.Answer.Evidence)

		listingsURL = probe.Answer.ListingsURL
		remoteConfirmed = probe.Answer.HasRemoteSoftwareRoles
		agentSec = orSeconds(probe.ElapsedSec, agentStarted)
		agentSteps = probe.Steps
		agentJudged = probe.Judged
		judgement = judgementNote(probe.Judgement)
		// The process as well as the answer: how many steps it took, how long, whether browser-use's own
		// trajectory check accepted the path, and what the agent said it saw. A run that got the right
		// answer the wrong way is the one worth finding, and it is invisible in an outcome alone.
		logger.Info("agent decided",
			slog.String("span", "agent"),
			slog.String("listings_url", listingsURL),
			slog.Bool("remote_confirmed", remoteConfirmed),
			slog.Int("role_count", probe.Answer.RemoteSoftwareRoleCount),
			slog.Int("agent_steps", probe.Steps),
			slog.Float64("agent_s", round1(agentSec)),
			slog.Bool("agent_successful", probe.Successful),
			slog.Bool("agent_judged", probe.Judged),
			slog.String("agent_judgement", judgement),
			slog.String("evidence", probe.Answer.Evidence),
		)

		blocked = probe.Answer.Blocked
		blockReason = probe.Answer.BlockReason

		// Cache the URL and the verdict together, whatever the verdict was. Caching only the positive
		// one - as this did before - left the majority of companies (the ones with nothing open)
		// re-running the agent on every single sweep.
		//
		// A BLOCK is the exception, and an important one: a challenge page produces the same
		// "no remote roles" answer as an empty board, and caching that would freeze a transient block
		// into a fact for the whole TTL. A blocked company is retried instead. A failed write is
		// logged, not fatal: the scrape itself is unaffected.
		if blocked {
			logger.Warn("not caching a blocked verdict",
				slog.String("span", "agent"),
				slog.String("block_reason", blockReason),
				slog.String("listings_url", listingsURL),
			)
		} else if err := store.SetCompanyListingsURL(ctx, database, company.ID, listingsURL, remoteConfirmed); err != nil {
			fmt.Fprintf(stderr, "scrape: cache listings url for %s: %v\n", company.Slug, err)
		}
	}

	// A URL the agent did not confirm remote roles at may not be a remote-filtered search page at all:
	// the agent is only told to apply a remote filter "if there is" one. Fetching it would store on-site
	// roles under is_remote = true, so the fetch is skipped rather than guessed at. The cost of that skip
	// is now paid once, not once per sweep, because the verdict above is cached with the URL - the next
	// sweep reaches this same point without running the agent.
	if !remoteConfirmed {
		finish("ok", 0, 0, 0, "")
		fmt.Fprintf(stdout, "run_id=%d company=%s no_remote_roles=true source=%s url=%s\n",
			runID, company.Slug, resolvedFrom, listingsURL)
		logger.Info("skip",
			slog.String("reason", "no_remote_roles"),
			slog.String("resolution", resolvedFrom),
			slog.String("listings_url", listingsURL),
			slog.Int("agent_steps", agentSteps),
			slog.Float64("agent_s", round1(agentSec)),
			slog.Bool("agent_judged", agentJudged),
			slog.String("agent_judgement", judgement),
		)
		return 0
	}

	// 2. Fetch: honor the listings origin's robots.txt too, then render and extract.
	listingsPolicy, err := robots.Fetch(ctx, httpClient, listingsURL, cfg.UserAgent)
	if err != nil {
		finish("error", 0, 0, 0, fmt.Sprintf("robots listings: %v", err))
		fmt.Fprintf(stderr, "scrape: robots %s: %v\n", listingsURL, err)
		return 1
	}
	if !listingsPolicy.Allowed(listingsURL, cfg.UserAgent) {
		finish("ok", 0, 0, 0, "")
		fmt.Fprintf(stdout, "run_id=%d company=%s robots=disallowed listings=%s\n",
			runID, company.Slug, listingsURL)
		return 0
	}
	crawlDelay := listingsPolicy.CrawlDelay(cfg.UserAgent)
	if careersCrawlDelay > crawlDelay {
		crawlDelay = careersCrawlDelay
	}
	if *minCrawlDelay > crawlDelay {
		crawlDelay = *minCrawlDelay
	}

	fetchArgs := []string{"worker/listings_fetch.py",
		"--url", listingsURL,
		"--max-jobs", fmt.Sprintf("%d", *maxJobs),
	}
	if crawlDelay > 0 {
		fetchArgs = append(fetchArgs, "--crawl-delay", fmt.Sprintf("%.2f", crawlDelay.Seconds()))
	}
	fetchCmd := exec.CommandContext(ctx, python, fetchArgs...)
	fetchCmd.Env = childEnv
	fetchStarted := time.Now()
	fetchOut, err := fetchCmd.Output()
	fetchSec = orSeconds(0, fetchStarted)
	if err != nil {
		finish("error", 0, 0, 0, fmt.Sprintf("fetch: %v", err))
		fmt.Fprintf(stderr, "scrape: fetch: %v\n", err)
		return 1
	}
	var fetched fetchOutput
	if err := json.Unmarshal(fetchOut, &fetched); err != nil {
		finish("error", 0, 0, 0, fmt.Sprintf("parse fetch output: %v", err))
		fmt.Fprintf(stderr, "scrape: parse fetch output: %v\n", err)
		return 1
	}
	// Diagnose a zero: how many links were on the rendered page vs how many the heuristic kept.
	fmt.Fprintf(stdout, "run_id=%d company=%s fetch: page_links=%d extracted=%d\n",
		runID, company.Slug, fetched.TotalLinks, len(fetched.Jobs))

	// A blocked listings page is a failure, not an empty board: it renders fine and yields nothing, so
	// reporting it as a successful zero would hide the block and let -skip-ok skip the company forever.
	if fetched.Blocked {
		finish("error", 0, 0, 0, fmt.Sprintf("blocked: %s", fetched.BlockReason))
		fmt.Fprintf(stdout, "run_id=%d company=%s blocked=1 reason=%s listings=%s\n",
			runID, company.Slug, fetched.BlockReason, listingsURL)
		logger.Warn("listings page blocked",
			slog.String("span", "fetch"),
			slog.String("block_reason", fetched.BlockReason),
			slog.String("listings_url", listingsURL),
		)
		return 1
	}
	// The worker reports its own elapsed_sec, which excludes parent/process startup; prefer it.
	fetchSec = orSeconds(fetched.ElapsedSec, fetchStarted)
	logger.Info("fetch",
		slog.String("span", "fetch"),
		slog.String("listings_url", listingsURL),
		slog.Int("page_links", fetched.TotalLinks),
		slog.Int("extracted", len(fetched.Jobs)),
		slog.Float64("fetch_s", round1(fetchSec)),
		slog.Float64("crawl_delay_s", crawlDelay.Seconds()),
	)

	extPattern := externalIDPattern(*vendor)

	// The observed set is what the company's board currently advertises: every posting this run found
	// linked on the listings page, keyed the way job_listings is (vendor platform + external_id). It
	// deliberately includes postings this run did not store - non-US ones, and ones whose detail page
	// failed to render - because those are still advertised, and staleness is about what the board no
	// longer offers. It mirrors the RemoteOK path, which extracts an element's identifier before
	// decoding the rest of the element so a partly-broken element is never mistaken for an absent one.
	observed := make(map[string]struct{}, len(fetched.Jobs))

	// The board's own structured data is read once per posting, in the same pass that builds the
	// observed set: posted_at and employment_type were being left NULL even though every posting
	// carries them in the JSON this run already fetched and stored. remoteEvidence counts the postings
	// that state their own remote status, which is the measurement that decides whether that field can
	// ever replace the agent's per-company verdict.
	postings := make([]jobposting.Posting, len(fetched.Jobs))
	remoteEvidence := 0
	// Posting pages that were blocked or failed, counted by reason. One line per sweep is enough to
	// see that a board is fighting us; one line per posting would drown everything else.
	blockedPostings := map[string]int{}
	erroredPostings := map[string]int{}
	for i, j := range fetched.Jobs {
		if j.URL != "" {
			observed[store.ExternalID(extPattern, j.URL)] = struct{}{}
		}
		if j.BlockReason != "" {
			blockedPostings[j.BlockReason]++
		}
		if j.Error != "" {
			erroredPostings[firstWords(j.Error, 6)]++
		}
		posting, ok := jobposting.Parse(j.RawData)
		if !ok {
			continue
		}
		postings[i] = posting
		if posting.Remote != nil {
			remoteEvidence++
		}
	}

	if len(blockedPostings) > 0 || len(erroredPostings) > 0 {
		logger.Warn("some postings were not readable",
			slog.String("span", "fetch"),
			slog.Any("blocked_by_reason", blockedPostings),
			slog.Any("errored_by_kind", erroredPostings),
			slog.Int("postings", len(fetched.Jobs)),
		)
	}

	// 3. Store: upsert the US postings (the deterministic US-only filter).
	storeStarted := time.Now()
	inserted, refreshed, skipped := 0, 0, 0
	for i, j := range fetched.Jobs {
		if j.URL == "" || j.Error != "" {
			continue
		}
		if j.IsUS == nil || !*j.IsUS {
			skipped++
			continue
		}
		job := store.BrowserJob{
			ExternalID:   store.ExternalID(extPattern, j.URL),
			ListingURL:   j.URL,
			Title:        j.Title,
			Description:  j.Description,
			RawData:      j.RawData,
			LocationText: strOrEmpty(j.LocationText),
			Country:      strOrEmpty(j.Country),
			IsUS:         j.IsUS,
			// is_remote stays true here on the strength of the URL: this branch is only reached when the
			// agent confirmed remote roles at a remote-filtered search URL, so every posting on it is a
			// remote opening. The posting's own jobLocationType is the evidence, not the gate.
			IsRemote:       true,
			EmploymentType: postings[i].EmploymentType,
			PostedAt:       postings[i].PostedAt,
		}
		listingID, isNew, err := store.UpsertBrowserJob(ctx, database, job, company.ID, jobPlatformID, runID)
		if err != nil {
			finish("error", int64(inserted+refreshed+skipped), int64(inserted), int64(refreshed), fmt.Sprintf("store: %v", err))
			fmt.Fprintf(stderr, "scrape: store %s: %v\n", j.URL, err)
			return 1
		}
		// One line per stored listing, carrying the id both apps key on.
		//
		// This is the record of what the pipeline actually produced, and it is the other half of the
		// cross-app join: apply-app stores jobs_listing_id for every application it makes, so
		// `{app=~"jobs-app|apply-app"} | json | listing_id = 12345` returns the scrape that found the job
		// and the application made from it. Without this line the query returns only apply-app's side,
		// which looks like a working query that is quietly missing half the story.
		logger.Info("listing stored",
			slog.String("span", "store"),
			slog.Int64("listing_id", listingID),
			slog.Bool("is_new", isNew),
			slog.String("title", firstChars(j.Title, 160)),
			slog.Bool("is_remote", job.IsRemote),
			slog.String("external_id", firstChars(job.ExternalID, 160)),
			slog.String("url", j.URL),
		)
		if isNew {
			inserted++
		} else {
			refreshed++
		}
	}

	storeSec = time.Since(storeStarted).Seconds()

	// 4. Stale-mark: close this company's postings on this vendor that the board no longer advertises,
	// so a dropped posting does not stay 'open' forever. Two guards keep a partial observation from
	// closing postings that are still live: an empty observed set closes nothing, and a run that hit
	// the -max-jobs cap saw only part of the board, so "not observed" there is not evidence of absence.
	// The residual limitation is the board's own rendering: a listings page that shows only its first
	// page of results looks identical to a complete one from here, so the closure is only as complete
	// as the page the agent landed on. Detecting that needs a completeness signal from the board (its
	// own posted result count) rather than another guard.
	closed := int64(0)
	staleNote := ""
	switch {
	case len(observed) == 0:
		staleNote = "none_observed"
	case len(fetched.Jobs) >= *maxJobs:
		staleNote = fmt.Sprintf("truncated_at_max_jobs=%d", *maxJobs)
	default:
		closed, err = store.MarkCompanyJobsStale(ctx, database, company.ID, jobPlatformID, observed)
		if err != nil {
			finish("error", int64(inserted+refreshed+skipped), int64(inserted), int64(refreshed), fmt.Sprintf("stale: %v", err))
			fmt.Fprintf(stderr, "scrape: stale %s: %v\n", company.Slug, err)
			return 1
		}
	}

	finish("ok", int64(inserted+refreshed+skipped), int64(inserted), int64(refreshed), "")
	logger.Info("store",
		slog.String("span", "store"),
		slog.Int("found", len(fetched.Jobs)),
		slog.Int("inserted", inserted),
		slog.Int("refreshed", refreshed),
		slog.Int("skipped_non_us", skipped),
		slog.Int("remote_evidence", remoteEvidence),
	)
	logger.Info("stale",
		slog.String("span", "stale"),
		slog.Int64("closed", closed),
		slog.String("note", staleNote),
	)
	// The report line, mirrored as one structured event so a whole sweep can be summarised from the log
	// store alone - without reading per-run files, which are empty during a sweep.
	logger.Info("summary",
		slog.String("span", "summary"),
		slog.String("resolution", resolvedFrom),
		slog.Bool("blocked", blocked),
		slog.String("block_reason", blockReason),
		slog.Int("agent_steps", agentSteps),
		slog.Float64("agent_s", round1(agentSec)),
		slog.Float64("fetch_s", round1(fetchSec)),
		slog.Float64("store_s", round1(storeSec)),
		slog.Float64("total_s", round1(time.Since(scrapeStarted).Seconds())),
		slog.Bool("agent_judged", agentJudged),
		slog.String("agent_judgement", judgement),
		slog.String("quality", qualityNote(resolvedFrom, remoteConfirmed, len(fetched.Jobs), inserted, skipped)),
		slog.String("listings_url", listingsURL),
		slog.Int("found", len(fetched.Jobs)),
		slog.Int("inserted", inserted),
		slog.Int("refreshed", refreshed),
		slog.Int("skipped_non_us", skipped),
		slog.Int64("closed", closed),
		slog.Int("remote_evidence", remoteEvidence),
		slog.String("stale_note", staleNote),
	)
	fmt.Fprintf(stdout, "run_id=%d company=%s listings=%s resolution=%s found=%d inserted=%d refreshed=%d skipped=%d closed=%d remote_evidence=%d",
		runID, company.Slug, listingsURL, resolvedFrom, len(fetched.Jobs), inserted, refreshed, skipped, closed, remoteEvidence)
	if staleNote != "" {
		fmt.Fprintf(stdout, " stale=skipped reason=%s", staleNote)
	}
	fmt.Fprintln(stdout)
	return 0
}

// firstWords reduces an error message to a bounded key, so a tally groups by kind rather than by the
// one detail that varies between two otherwise identical failures.
func firstWords(text string, n int) string {
	fields := strings.Fields(text)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

// externalIDPattern reads the vendor playbook's external_id_pattern, or "" when absent.
func externalIDPattern(vendor string) string {
	raw, err := os.ReadFile(fmt.Sprintf("worker/vendor-playbooks/%s.json", vendor))
	if err != nil {
		return ""
	}
	var pb struct {
		ExternalIDPattern string `json:"external_id_pattern"`
	}
	if json.Unmarshal(raw, &pb) != nil {
		return ""
	}
	return pb.ExternalIDPattern
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// reuseCachedListingsURL decides whether a previously resolved filtered listings URL can stand in for
// a fresh agent run. It returns the URL to use and true when the cache is usable.
//
// The cache is usable when a URL was stored, the caller did not ask to re-resolve, and the stored
// resolution is newer than ttl. ttl <= 0 disables reuse, which is how a caller opts back into the
// always-run-the-agent behavior without a second flag.
//
// An unparseable timestamp is treated as stale rather than trusted: one extra agent run is cheap next
// to reusing a URL whose age is unknown.
func reuseCachedListingsURL(c store.ScrapeCompany, now time.Time, ttl time.Duration, refresh bool) (string, bool) {
	if refresh || c.ListingsURL == "" || ttl <= 0 {
		return "", false
	}
	resolvedAt, err := time.Parse(time.RFC3339, c.ListingsURLResolvedAt)
	if err != nil {
		return "", false
	}
	if now.Sub(resolvedAt) > ttl {
		return "", false
	}
	return c.ListingsURL, true
}

// appendTraceEvent appends one JSON object to a run's trace file, creating the directory and file if
// they do not exist. The Python worker writes its own events in this same JSONL shape; this is how a
// run that skipped the agent still leaves a trace that says so. Tracing is best-effort everywhere, so
// a failure here is swallowed rather than failing a scrape that otherwise succeeded.
func appendTraceEvent(path string, event map[string]any) {
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(raw, '\n'))
}

// ollamaReachable probes the model host with a lightweight GET /api/tags (the cheapest "is Ollama
// up" check) up to three times with a short backoff, and returns nil on the first success. Three
// retries is the stop-when-down contract: a transient blip recovers, a genuinely down host fails
// the company instead of the agent hanging on its first LLM call.
func ollamaReachable(ctx context.Context, client *http.Client, host string) error {
	url := strings.TrimSuffix(host, "/") + "/api/tags"
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("ollama probe: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("ollama responded %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	return fmt.Errorf("ollama unreachable after 3 retries (%s): %w", url, lastErr)
}
