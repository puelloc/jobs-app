// Package runresolve drives one resolution run: pick the companies, resolve each, persist the
// outcome, and report counters.
//
// It sits between the tiers in internal/careers, which are pure, and the command, which is thin. The
// work it does that no tier can is the part with side effects and order: which companies get looked
// at, which tier answers, how evidence is retained, and what the run counters mean.
//
// design: docs/sp1500-plan.md, sections 7.1-7.3.
package runresolve

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"jobsapp/internal/careers"
	"jobsapp/internal/careers/homepage"
	"jobsapp/internal/store"
)

// PlatformID is the platforms row a resolution run is recorded against: 23, career_resolution, a
// reference source. It is not a job board or an applicant system, which is why the 'reference' type
// was added.
const PlatformID = 23

// Company is a company to resolve.
type Company struct {
	ID   int64
	Slug string
	Name string
	// Article is the enwiki title, which tier 1 needs. Empty means tier 1 cannot help and an
	// existing website is required.
	Article string
	// Website is the company's homepage, when one is already known.
	Website string
	// CareerSiteURL is set for a company that already has one, used by the skip rules.
	CareerSiteURL string
}

// Options control one run.
type Options struct {
	// DryRun records attempts and evidence but changes no company.
	DryRun bool
	// ResolveHomepages enables tier 1 for companies with no website.
	//
	// Off by default, and deliberately so: tier 1 is an enrichment that costs two API round trips
	// per batch, and a run over companies that already have a website gets nothing from it. Leaving
	// it on by default made the wiring resolve homepages in tests that had no fixtures for the APIs,
	// which is how this default was discovered.
	ResolveHomepages bool
	// OnlySlugs restricts the run to these slugs. Empty means no restriction.
	OnlySlugs []string
	// Limit caps how many companies are processed. Zero means no cap.
	Limit int
	// Refresh re-resolves companies that already have a career site URL.
	Refresh bool
	// RefreshFailed resolves only companies that do not have one.
	RefreshFailed bool
	// Concurrency bounds simultaneous workers. Zero means the default.
	Concurrency int
	// DataDir is where evidence is written.
	DataDir string
	// Progress, when set, is called once per company as its result lands. It is called from the
	// single run goroutine, so the callback need not be concurrency-safe.
	Progress func(Progress)
}

// Progress is one company's resolution outcome, reported as it lands.
type Progress struct {
	Slug     string
	Resolved bool
	Err      error
	// Reason explains an unresolved company: the last non-accepted attempt's reason, or
	// "no_candidates" when nothing was ever proposed. Without it the only signal is "unresolved",
	// which reads the same whether a company has no findable careers site or every candidate it
	// proposed failed validation - two problems with different fixes.
	Reason string
	// Attempts and Sources say how hard the resolver tried and where the candidates came from, which
	// is what turns "unresolved" into something actionable.
	Attempts int
	Sources  []string
}

// Summary is what a completed run did. The counters match scrape_runs.
type Summary struct {
	RunID int64
	// Found is the companies considered, excluding those skipped.
	Found int
	// Inserted is companies whose career_site_url went from NULL to a value.
	Inserted int
	// Updated is companies whose career_site_url changed value.
	Updated int
	// Resolved and Unresolved partition Found by whether a careers URL was accepted.
	Resolved   int
	Unresolved int
	// Skipped is companies excluded before any request, such as ones already resolved.
	Skipped int
	// Collisions counts evidence-path collisions, which the exit-5 rule reports.
	Collisions int
	// Failed counts companies whose resolution or persistence errored, as distinct from companies
	// that resolved cleanly to nothing.
	Failed int
	// FirstError is the first failure, kept so a run where everything failed can say why instead of
	// reporting a bare zero.
	FirstError string
	Duration   time.Duration
}

// Runner executes a resolution run.
type Runner struct {
	DB      *sql.DB
	Fetcher careers.Fetcher
	// Wikipedia and Wikidata are the tier-1 API roots; empty means the defaults.
	Wikipedia string
	Wikidata  string
	// Now is injectable so a test can pin the run's start time.
	Now func() time.Time
}

// Run resolves the given companies and persists the results.
//
// It returns the summary and an error. A company that fails does not abort the run: the failure is
// recorded against that company and the run continues, because a 1,500-company pass that stops at
// the first bad host is not usable.
func (r Runner) Run(ctx context.Context, companies []Company, opts Options) (Summary, error) {
	var sum Summary
	if r.DB == nil {
		return sum, fmt.Errorf("runresolve: Runner.DB is nil")
	}
	if r.Fetcher == nil {
		return sum, fmt.Errorf("runresolve: Runner.Fetcher is nil")
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}

	work := r.selectWork(companies, opts, &sum)
	if len(work) == 0 {
		// Nothing to do is not a failure, but it is also not a run: no scrape_runs row is written,
		// so an operator cannot mistake a no-op for a pass that did work.
		return sum, nil
	}

	started := now()
	var runID int64
	var err error
	if opts.DryRun {
		runID, _, err = store.StartDryRun(ctx, r.DB, PlatformID)
	} else {
		runID, _, err = store.StartRun(ctx, r.DB, PlatformID)
	}
	if err != nil {
		return sum, fmt.Errorf("start run: %w", err)
	}
	sum.RunID = runID

	writer := store.NewResolutionWriter(r.DB, opts.DataDir)
	client := &homepage.Client{Fetcher: r.Fetcher, Wikipedia: r.Wikipedia, Wikidata: r.Wikidata}

	// Tier 1 runs once for the whole run rather than per company: it is batched, so 1,500 titles
	// cost ~30 requests, whereas per-company lookups would cost 1,500.
	var homepages map[string]homepage.Result
	if opts.ResolveHomepages {
		homepages = r.resolveHomepages(ctx, client, work)
	}

	for _, c := range work {
		// progressFor turns a finished company into what the caller's log needs, including the reason
		// an unresolved one was not resolved.
		res, err := r.resolveOne(ctx, client, c, homepages[c.Article], sum.RunID, opts)
		if err != nil {
			// The run continues - a 1,500-company pass that stops at the first bad host is not
			// usable - but the failure is counted and the first one is kept, so a run where
			// everything failed can say why rather than reporting a bare zero.
			sum.Unresolved++
			sum.Failed++
			if sum.FirstError == "" {
				sum.FirstError = fmt.Sprintf("%s: %v", c.Slug, err)
			}
			if opts.Progress != nil {
				opts.Progress(Progress{Slug: c.Slug, Err: err})
			}
			continue
		}

		out, err := writer.Write(ctx, sum.RunID, 0, res)
		if err != nil {
			sum.Unresolved++
			sum.Failed++
			if sum.FirstError == "" {
				sum.FirstError = fmt.Sprintf("%s: %v", c.Slug, err)
			}
			if opts.Progress != nil {
				opts.Progress(Progress{Slug: c.Slug, Err: err})
			}
			continue
		}
		if out.CareerSiteInserted {
			sum.Inserted++
		}
		if out.CareerSiteUpdated {
			sum.Updated++
		}
		if res.Resolved() {
			sum.Resolved++
		} else {
			sum.Unresolved++
		}
		if opts.Progress != nil {
			opts.Progress(progressFor(c.Slug, res))
		}
	}

	sum.Found = len(work)
	sum.Duration = time.Since(started)

	status := "ok"
	var errText *string
	if sum.Resolved == 0 {
		// The run completed and produced nothing. That is exit 4 territory rather than an error,
		// but the run row should say so rather than claim a clean success - and if anything errored,
		// the first error is what an operator needs.
		msg := "no companies resolved"
		if sum.FirstError != "" {
			msg = "no companies resolved; first failure: " + sum.FirstError
		}
		errText = &msg
	}
	if err := store.FinishRun(ctx, r.DB, sum.RunID, status,
		int64(sum.Found), int64(sum.Inserted), int64(sum.Updated), errText); err != nil {
		return sum, fmt.Errorf("finish run: %w", err)
	}
	return sum, nil
}

// selectWork applies the skip rules and returns the companies to resolve.
//
// The rules are here rather than in SQL because they interact with the flags, and a misread flag
// combination silently resolving the wrong set is worse than an explicit loop.
// progressFor describes a finished company for the caller's log.
func progressFor(slug string, res store.Resolution) Progress {
	p := Progress{Slug: slug, Resolved: res.Resolved(), Attempts: len(res.Attempts)}
	seen := map[string]bool{}
	for _, attempt := range res.Attempts {
		if attempt.Source != "" && !seen[attempt.Source] {
			seen[attempt.Source] = true
			p.Sources = append(p.Sources, attempt.Source)
		}
	}
	if p.Resolved {
		return p
	}
	if len(res.Attempts) == 0 {
		p.Reason = "no_candidates"
		return p
	}
	p.Reason = "no_acceptable_candidate"
	if reason := string(res.Attempts[len(res.Attempts)-1].Reason); reason != "" {
		p.Reason = reason
	}
	return p
}

func (r Runner) selectWork(companies []Company, opts Options, sum *Summary) []Company {
	allowed := map[string]bool{}
	for _, slug := range opts.OnlySlugs {
		if s := strings.TrimSpace(slug); s != "" {
			allowed[s] = true
		}
	}

	var out []Company
	for _, c := range companies {
		if len(allowed) > 0 && !allowed[c.Slug] {
			sum.Skipped++
			continue
		}
		// Refresh-failed is the narrower rule: only companies with no career site URL yet.
		if opts.RefreshFailed && c.CareerSiteURL != "" {
			sum.Skipped++
			continue
		}
		// A company already resolved is skipped unless the run asks to refresh. Without this, a
		// weekly pass would re-fetch 1,200 settled companies every time.
		if c.CareerSiteURL != "" && !opts.Refresh && !opts.RefreshFailed {
			sum.Skipped++
			continue
		}
		out = append(out, c)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out
}

// companyRow is the subset of companies a run needs to choose and resolve.
//
// article_title is coalesced rather than read as NULL because the resolver works in plain strings;
// the empty-string-versus-NULL distinction is meaningful in the database, not here.
const companyQuery = `
SELECT id, slug, name, coalesce(career_site_url,''),
       coalesce(website,''), coalesce(article_title,'')
  FROM companies
 ORDER BY slug`

// LoadCompanies reads the companies a run can consider, applying the only-slugs filter in SQL so a
// restricted run does not read the whole table.
func LoadCompanies(ctx context.Context, db *sql.DB, onlySlugs []string) ([]Company, error) {
	query := companyQuery
	args := []any{}
	if len(onlySlugs) > 0 {
		placeholders := make([]string, 0, len(onlySlugs))
		for _, slug := range onlySlugs {
			placeholders = append(placeholders, "?")
			args = append(args, strings.TrimSpace(slug))
		}
		query = `
SELECT id, slug, name, coalesce(career_site_url,''),
       coalesce(website,''), coalesce(article_title,'')
  FROM companies
 WHERE slug IN (` + strings.Join(placeholders, ",") + `)
 ORDER BY slug`
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load companies: %w", err)
	}
	defer rows.Close()

	var out []Company
	for rows.Next() {
		var c Company
		var article string
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.CareerSiteURL, &c.Website, &article); err != nil {
			return nil, fmt.Errorf("scan company: %w", err)
		}
		// Tier 1 keys on the article the index page actually linked to, which is often not the
		// security name: the S&P page says "Advanced Micro Devices" and links to [[AMD]]. The name is
		// the fallback for the rows whose Security cell carried no wikilink, which is the best
		// identity those rows have.
		c.Article = article
		if c.Article == "" {
			c.Article = c.Name
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate companies: %w", err)
	}
	return out, nil
}
