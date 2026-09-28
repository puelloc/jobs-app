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

	// 1. Agent: find the filtered listings URL, writing the live trace as it goes. Pre-flight the
	// model host first so a down Ollama fails this company fast instead of launching Chromium and
	// then hanging on the first LLM call.
	if err := ollamaReachable(ctx, probeClient, cfg.OllamaHost); err != nil {
		finish("error", 0, 0, 0, err.Error())
		fmt.Fprintf(stderr, "scrape: %v\n", err)
		return exitcode.OllamaUnreachable
	}

	probeOut, err := exec.CommandContext(ctx, python, "worker/remote_roles_probe.py",
		"--url", company.CareerSiteURL,
		"--company", company.Name,
		"--host", cfg.OllamaHost,
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

	// 2. Fetch: honor the listings origin's robots.txt too, then render and extract.
	listingsPolicy, err := robots.Fetch(ctx, httpClient, probe.Answer.ListingsURL, cfg.UserAgent)
	if err != nil {
		finish("error", 0, 0, 0, fmt.Sprintf("robots listings: %v", err))
		fmt.Fprintf(stderr, "scrape: robots %s: %v\n", probe.Answer.ListingsURL, err)
		return 1
	}
	if !listingsPolicy.Allowed(probe.Answer.ListingsURL, cfg.UserAgent) {
		finish("ok", 0, 0, 0, "")
		fmt.Fprintf(stdout, "run_id=%d company=%s robots=disallowed listings=%s\n",
			runID, company.Slug, probe.Answer.ListingsURL)
		return 0
	}
	crawlDelay := listingsPolicy.CrawlDelay(cfg.UserAgent)
	if d := careersPolicy.CrawlDelay(cfg.UserAgent); d > crawlDelay {
		crawlDelay = d
	}
	if *minCrawlDelay > crawlDelay {
		crawlDelay = *minCrawlDelay
	}

	fetchArgs := []string{"worker/listings_fetch.py",
		"--url", probe.Answer.ListingsURL,
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
		_, isNew, err := store.UpsertBrowserJob(ctx, database, job, company.ID, jobPlatformID)
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

	finish("ok", int64(inserted+refreshed+skipped), int64(inserted), int64(refreshed), "")
	fmt.Fprintf(stdout, "run_id=%d company=%s listings=%s found=%d inserted=%d refreshed=%d skipped=%d\n",
		runID, company.Slug, probe.Answer.ListingsURL, len(fetched.Jobs), inserted, refreshed, skipped)
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
