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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/exitcode"
	"jobsapp/internal/robots"
	"jobsapp/internal/store"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type probeAnswer struct {
	ListingsURL             string `json:"listings_url"`
	HasRemoteSoftwareRoles  bool   `json:"has_remote_software_roles"`
	RemoteSoftwareRoleCount int    `json:"remote_software_role_count"`
	Evidence                string `json:"evidence"`
}

type probeOutput struct {
	OK      bool         `json:"ok"`
	Timeout bool         `json:"timeout"`
	Error   string       `json:"error"`
	Answer  *probeAnswer `json:"answer"`
}

type fetchJob struct {
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	RawData      string  `json:"raw_data"`
	Error        string  `json:"error"`
	Country      *string `json:"country"`
	LocationText *string `json:"location_text"`
	IsUS         *bool   `json:"is_us"`
}

type fetchOutput struct {
	Jobs       []fetchJob `json:"jobs"`
	TotalLinks int        `json:"total_links"`
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
	if cached, ok := reuseCachedListingsURL(company, time.Now().UTC(), *listingsURLTTL, *refreshListingsURL); ok {
		listingsURL, resolvedFrom = cached, "cache"
		// The cached path never runs the agent, so this run would otherwise have no trace at all -
		// and the run page, the -skip-traced sweep option, and the job page's "why did this match"
		// section all read one. Record the resolution the agent would have produced.
		appendTraceEvent(trace, map[string]any{
			"event":        "resolution",
			"source":       "cache",
			"listings_url": cached,
			"resolved_at":  company.ListingsURLResolvedAt,
		})
		fmt.Fprintf(stdout, "run_id=%d company=%s listings_url=cache resolved_at=%s url=%s\n",
			runID, company.Slug, company.ListingsURLResolvedAt, cached)
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

		probeOut, err := exec.CommandContext(ctx, python, "worker/remote_roles_probe.py",
			"--url", company.CareerSiteURL,
			"--company", company.Name,
			"--host", cfg.OllamaHost,
			"--model", cfg.BrowserUseModel,
			"--max-steps", fmt.Sprintf("%d", *maxSteps),
			"--trace", trace,
		).Output()
		if err != nil {
			// The worker process failed. Re-check the host: if Ollama is down now, that is the cause
			// and the batch runner should stop; otherwise it is an ordinary agent failure.
			fmt.Fprintf(stderr, "scrape: agent: %v\n", err)
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
			return 0
		}

		// Log what the agent decided: its evidence and the count it saw, so a later zero is diagnosable.
		fmt.Fprintf(stdout, "run_id=%d company=%s agent: has_remote=%t count=%d evidence=%q\n",
			runID, company.Slug, probe.Answer.HasRemoteSoftwareRoles,
			probe.Answer.RemoteSoftwareRoleCount, probe.Answer.Evidence)

		// If the agent found no remote roles, there is nothing to scrape. Skip the fetch rather than
		// extracting onsite postings and marking them remote.
		if !probe.Answer.HasRemoteSoftwareRoles {
			finish("ok", 0, 0, 0, "")
			fmt.Fprintf(stdout, "run_id=%d company=%s no_remote_roles=true found=0 evidence=%q\n",
				runID, company.Slug, probe.Answer.Evidence)
			return 0
		}

		listingsURL = probe.Answer.ListingsURL

		// Cache the resolution so the next sweep can skip the agent. A failed write is logged, not
		// fatal: the scrape itself is unaffected, and the only cost is re-running the agent next time.
		if err := store.SetCompanyListingsURL(ctx, database, company.ID, listingsURL); err != nil {
			fmt.Fprintf(stderr, "scrape: cache listings url for %s: %v\n", company.Slug, err)
		}
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
	fetchOut, err := exec.CommandContext(ctx, python, fetchArgs...).Output()
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

	extPattern := externalIDPattern(*vendor)

	// The observed set is what the company's board currently advertises: every posting this run found
	// linked on the listings page, keyed the way job_listings is (vendor platform + external_id). It
	// deliberately includes postings this run did not store - non-US ones, and ones whose detail page
	// failed to render - because those are still advertised, and staleness is about what the board no
	// longer offers. It mirrors the RemoteOK path, which extracts an element's identifier before
	// decoding the rest of the element so a partly-broken element is never mistaken for an absent one.
	observed := make(map[string]struct{}, len(fetched.Jobs))
	for _, j := range fetched.Jobs {
		if j.URL != "" {
			observed[store.ExternalID(extPattern, j.URL)] = struct{}{}
		}
	}

	// 3. Store: upsert the US postings (the deterministic US-only filter).
	inserted, refreshed, skipped := 0, 0, 0
	for _, j := range fetched.Jobs {
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
			IsRemote:     true,
		}
		_, isNew, err := store.UpsertBrowserJob(ctx, database, job, company.ID, jobPlatformID, runID)
		if err != nil {
			finish("error", int64(inserted+refreshed+skipped), int64(inserted), int64(refreshed), fmt.Sprintf("store: %v", err))
			fmt.Fprintf(stderr, "scrape: store %s: %v\n", j.URL, err)
			return 1
		}
		if isNew {
			inserted++
		} else {
			refreshed++
		}
	}

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
	fmt.Fprintf(stdout, "run_id=%d company=%s listings=%s resolution=%s found=%d inserted=%d refreshed=%d skipped=%d closed=%d",
		runID, company.Slug, listingsURL, resolvedFrom, len(fetched.Jobs), inserted, refreshed, skipped, closed)
	if staleNote != "" {
		fmt.Fprintf(stdout, " stale=skipped reason=%s", staleNote)
	}
	fmt.Fprintln(stdout)
	return 0
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
