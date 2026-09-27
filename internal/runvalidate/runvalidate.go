// Package runvalidate drives the browser-use validation pass: take every company with a stored
// career_site_url, load it in a real browser, and judge what came back with the same gate the
// resolution tiers use.
//
// It is deliberately not a second resolver. The resolution ladder answers "what is this company's
// careers URL?"; this answers "is the URL we stored still that?". So it never writes career_site_url.
// It records one attempt row per fetch, moves the company's last-verified stamp, and leaves the
// stored value alone even when the verdict is that the page is not a careers page. A validation run
// that silently rewrote or cleared URLs would destroy the very thing it exists to measure.
//
// Two tiers, in the order the plan's M3 section calls for:
//
//  1. Render. A real Chromium loads the stored URL and the gate judges the rendered document. This
//     is what a plain HTTP client cannot do: JavaScript-rendered job lists appear, and pages that
//     answer a scriptless client with an empty shell or a bot wall render normally.
//  2. Escalate. When the render never produced a page at all, a browser-use agent may navigate to
//     the company's actual listings. Its answer is a URL, and that URL is re-rendered and gated -
//     the LLM never decides whether a page is a careers page.
//
// design: docs/sp1500-plan.md, sections 7.2 and 7.5.
package runvalidate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"jobsapp/internal/browseruse"
	"jobsapp/internal/careers"
	"jobsapp/internal/store"
)

// PlatformID is the platforms row a validation run is recorded against: 24, career_validation,
// seeded by migration 009. It is distinct from 23 (career_resolution) because the two runs answer
// different questions, and a reporting query that could not tell them apart would count a
// validation sweep as a resolution pass.
const PlatformID = 24

// Source values written to url_resolution_attempts.source. They are the tier labels the plan's
// schema comment already anticipated ("nav_anchor", "robots_sitemap", "browser_use", ...).
const (
	// SourceRender is a page fetched by the real browser.
	SourceRender = "browser_use"
	// SourceAgent is a page reached after the agent navigated from the stored URL. It is a separate
	// source so a report can separate "the stored URL is right" from "the stored URL pointed
	// somewhere that led to the right place".
	SourceAgent = "browser_use_agent"
)

// Category is the verdict class a company falls into after validation.
//
// The three-way split is the census's, kept deliberately: "wrong" means the browser received a page
// and that page is demonstrably not a careers page, while everything else that failed to confirm is
// "unverifiable" - a blocked, dead or timed-out site proves nothing about the stored URL. Folding
// the two together is how a measurement turns a 403 into a false accusation.
type Category string

const (
	// CategoryConfirmed means the gate accepted the page.
	CategoryConfirmed Category = "confirmed"
	// CategoryWrong means the page loaded and is not a careers page.
	CategoryWrong Category = "wrong"
	// CategoryUnverifiable means no page was obtained, so the stored URL is neither confirmed nor
	// disproved.
	CategoryUnverifiable Category = "unverifiable"
)

// Company is a company whose stored careers URL is being validated.
type Company struct {
	ID            int64
	Slug          string
	Name          string
	CareerSiteURL string
}

// Options control one run.
type Options struct {
	// DryRun records attempts but moves no company stamp.
	DryRun bool
	// OnlySlugs restricts the run to these slugs. Empty means every company with a stored URL.
	OnlySlugs []string
	// Limit caps how many companies are processed. Zero means no cap.
	Limit int
	// Concurrency bounds simultaneous browser workers. Zero means one, which is what the plan
	// specifies: the browser tier is the heavy one, and a sweep of 600 sites is not urgent.
	Concurrency int
	// Escalate enables the browser-use agent tier for pages that never rendered.
	Escalate bool
	// DataDir is where non-accepted attempts' response bodies are retained. Empty disables evidence.
	DataDir string
	// MaxBodyBytes bounds the rendered body kept for the gate and for evidence.
	MaxBodyBytes int64
	// RenderTimeoutSeconds is the browser's per-page budget.
	RenderTimeoutSeconds float64
	// AgentMaxSteps bounds the escalation loop.
	AgentMaxSteps int
	// EvidenceBodyBytes retains this many bytes of an accepted attempt's response for re-judging
	// under future rules. Zero keeps only rejected attempts' bodies, which is the older behaviour;
	// a rejected attempt always keeps everything the gate saw.
	EvidenceBodyBytes int64
	// Progress, when set, is called once per company as its result is written. It is called from the
	// single writer goroutine, so an implementation needs no locking: a sweep of 600 sites can take
	// an hour, and a run that prints nothing until it finishes is indistinguishable from a hang.
	Progress func(Progress)
}

// Progress is one company's outcome, reported as it lands.
type Progress struct {
	Slug string
	// Category is the verdict class. Empty when the company could not be validated at all.
	Category Category
	// Reason is the gate's rejection reason, empty for a confirmed company.
	Reason careers.Reason
	// HTTPStatus is what the browser saw, zero when no response was received.
	HTTPStatus int
	// Escalated reports that the agent tier ran for this company.
	Escalated bool
	// Err is set when the company could not be validated at all.
	Err error
}

// Summary is what a completed run did.
type Summary struct {
	RunID int64
	// Found is the companies considered.
	Found int
	// Confirmed, Wrong and Unverifiable partition Found.
	Confirmed    int
	Wrong        int
	Unverifiable int
	// Escalated counts companies where the agent tier actually ran.
	Escalated int
	// Failed counts companies whose validation could not be carried out at all - a worker that
	// would not start, a database write that failed - as distinct from a company that validated to
	// a negative result.
	Failed int
	// FirstError is the first failure, kept so a run where everything failed can say why instead of
	// reporting a bare zero.
	FirstError string
	Duration   time.Duration
}

// Runner executes a validation run.
type Runner struct {
	DB *sql.DB
	// Renderer loads a page in a real browser. Required.
	Renderer browseruse.Renderer
	// Agent navigates on behalf of the browser when a page does not render. Optional; when nil,
	// escalation is unavailable and every unresolved page stays unverifiable.
	Agent browseruse.Renderer
	// Now is injectable so a test can pin the run's start time.
	Now func() time.Time
}

// Run validates the given companies and persists the results.
//
// A company that fails does not abort the run: the failure is counted and the first one is kept, in
// the same shape runresolve uses, because a 600-URL sweep that stops at the first bad host is not
// usable.
func (r Runner) Run(ctx context.Context, companies []Company, opts Options) (Summary, error) {
	var sum Summary
	if r.DB == nil {
		return sum, errors.New("runvalidate: Runner.DB is nil")
	}
	if r.Renderer == nil {
		return sum, errors.New("runvalidate: Runner.Renderer is nil")
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}

	work := selectWork(companies, opts)
	if len(work) == 0 {
		// Nothing to do is not a failure, and it is not a run either: no scrape_runs row is written,
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

	// Renders run concurrently; the writer does not. db.Open caps the pool at one connection, so a
	// concurrent writer would contend on it and interleave one company's attempts with another's.
	// The single consumer below makes the serialization explicit rather than accidental.
	//
	// The semaphore is acquired *inside* the goroutine, never in this loop. Acquiring it here looks
	// equivalent and is not: it couples spawning to consuming, so once `concurrency` workers are
	// blocked writing into a full results buffer, this loop blocks on the semaphore and the drain
	// loop below it is never reached. That is a deadlock, and it fires on exactly the input sizes
	// where more companies remain than `concurrency + cap(results)` - a live three-company run at
	// concurrency 1 hung on it while an eight-company test at concurrency 4 passed by arithmetic
	// luck. The buffer is sized to the concurrency so a worker holding a slot always has room to
	// deposit its result before releasing that slot.
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	results := make(chan outcome, concurrency)
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for _, c := range work {
		wg.Add(1)
		go func(c Company) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results <- r.judge(ctx, c, opts)
		}(c)
	}
	go func() { wg.Wait(); close(results) }()

	for out := range results {
		sum.Found++
		if out.escalated {
			sum.Escalated++
		}
		if out.err != nil {
			sum.Failed++
			if sum.FirstError == "" {
				sum.FirstError = fmt.Sprintf("%s: %v", out.company.Slug, out.err)
			}
			if opts.Progress != nil {
				opts.Progress(Progress{Slug: out.company.Slug, Escalated: out.escalated, Err: out.err})
			}
			continue
		}

		if _, err := writer.Write(ctx, runID, 0, out.resolution(opts.DryRun)); err != nil {
			sum.Failed++
			if sum.FirstError == "" {
				sum.FirstError = fmt.Sprintf("%s: %v", out.company.Slug, err)
			}
			if opts.Progress != nil {
				opts.Progress(Progress{Slug: out.company.Slug, Escalated: out.escalated, Err: err})
			}
			continue
		}

		switch out.category {
		case CategoryConfirmed:
			sum.Confirmed++
		case CategoryWrong:
			sum.Wrong++
		default:
			sum.Unverifiable++
		}
		if opts.Progress != nil {
			opts.Progress(out.progress())
		}
	}

	sum.Duration = time.Since(started)

	// items_inserted carries the confirmed count because it is the closest thing this table has to
	// "the stored value was verified this run"; items_updated stays zero because a validation run
	// changes no company's careers URL. The other two classes go in their own columns, added with
	// this pass, so the run row states the whole split rather than only the good news. The per-company
	// record is url_resolution_attempts, which is what can be queried company by company.
	status := "ok"
	var errText *string
	if sum.Failed > 0 {
		status = "error"
		msg := fmt.Sprintf("%d companies could not be validated; first failure: %s", sum.Failed, sum.FirstError)
		errText = &msg
	}
	if err := store.FinishValidationRun(ctx, r.DB, runID, status,
		int64(sum.Found), int64(sum.Confirmed), int64(sum.Wrong), int64(sum.Unverifiable), errText); err != nil {
		return sum, fmt.Errorf("finish run: %w", err)
	}
	return sum, nil
}

// selectWork applies the only-slugs and limit filters.
func selectWork(companies []Company, opts Options) []Company {
	allowed := map[string]bool{}
	for _, slug := range opts.OnlySlugs {
		if s := strings.TrimSpace(slug); s != "" {
			allowed[s] = true
		}
	}
	var out []Company
	for _, c := range companies {
		if len(allowed) > 0 && !allowed[c.Slug] {
			continue
		}
		out = append(out, c)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out
}

// outcome is one company's judged result, carried from a render worker to the writer.
type outcome struct {
	company   Company
	attempts  []store.ResolutionAttempt
	category  Category
	escalated bool
	err       error
}

// progress reports the outcome for the run log. It reads the first attempt, which is the stored
// URL's own verdict: an escalation's verdict describes a different URL and would misreport what the
// stored value did.
func (o outcome) progress() Progress {
	p := Progress{Slug: o.company.Slug, Category: o.category, Escalated: o.escalated}
	for _, a := range o.attempts {
		if a.Source == SourceRender {
			p.Reason = a.Reason
			p.HTTPStatus = a.HTTPStatus
			break
		}
	}
	return p
}

// resolution turns the outcome into the value the store writes.
//
// CareerSiteURL is deliberately absent: a validation run must not rewrite the stored value. The
// status and title stamps do move, because "last verified" is exactly what this run establishes.
func (o outcome) resolution(dryRun bool) store.Resolution {
	res := store.Resolution{
		CompanyID:   o.company.ID,
		CompanySlug: o.company.Slug,
		DryRun:      dryRun,
		Attempts:    o.attempts,
		// The verdict is the company-level answer, written in the same transaction as the attempts
		// so the "what do we currently believe about this URL" column can never disagree with the
		// trail behind it. The URL travels with it: without it a later resolver that changes
		// career_site_url would leave behind a verdict about a page nobody stores, and the skip rule
		// would treat that company as settled forever.
		CareerSiteVerdict:    string(o.category),
		CareerSiteVerdictURL: o.company.CareerSiteURL,
	}
	for _, a := range o.attempts {
		if a.Source == SourceRender {
			res.CareerSiteStatus = a.HTTPStatus
			res.CareerSiteTitle = a.Title
			break
		}
	}
	return res
}

// judge renders the stored URL, gates what came back, and escalates when the page never arrived.
func (r Runner) judge(ctx context.Context, c Company, opts Options) outcome {
	out := outcome{company: c}

	res, err := r.Renderer.Render(ctx, browseruse.Request{
		Mode:           browseruse.ModeRender,
		URL:            c.CareerSiteURL,
		CompanyName:    c.Name,
		TimeoutSeconds: opts.RenderTimeoutSeconds,
		MaxBodyBytes:   opts.MaxBodyBytes,
	})
	if err != nil {
		// A worker that would not run is not a fact about this site; every company would fail the
		// same way, which is why it is a run failure and not a verdict.
		out.err = err
		return out
	}

	verdict := r.gate(c, c.CareerSiteURL, SourceRender, res)
	out.attempts = append(out.attempts, attemptFor(c, SourceRender, c.CareerSiteURL, res, verdict, opts.EvidenceBodyBytes))
	out.category = classify(verdict)

	if !opts.Escalate || r.Agent == nil || !shouldEscalate(verdict.Reason) {
		return out
	}

	out.escalated = true
	agentRes, agentErr := r.Agent.Render(ctx, browseruse.Request{
		Mode:        browseruse.ModeAgent,
		URL:         c.CareerSiteURL,
		CompanyName: c.Name,
		Agent: &browseruse.AgentOptions{
			MaxSteps: opts.AgentMaxSteps,
		},
	})
	if agentErr != nil || !agentRes.OK {
		// Escalation failing says nothing about the company. The first verdict stands, and the
		// reason is available on stderr from the worker.
		return out
	}
	if sameURL(agentRes.FinalURL, c.CareerSiteURL) {
		// The agent ended where it started, so there is nothing new to render and the first verdict
		// stands. Reporting agreement is useful, but it is not a second attempt.
		return out
	}

	second, err := r.Renderer.Render(ctx, browseruse.Request{
		Mode:           browseruse.ModeRender,
		URL:            agentRes.FinalURL,
		CompanyName:    c.Name,
		TimeoutSeconds: opts.RenderTimeoutSeconds,
		MaxBodyBytes:   opts.MaxBodyBytes,
	})
	if err != nil {
		return out
	}
	agentVerdict := r.gate(c, agentRes.FinalURL, SourceAgent, second)
	out.attempts = append(out.attempts, attemptFor(c, SourceAgent, agentRes.FinalURL, second, agentVerdict, opts.EvidenceBodyBytes))

	// Only an acceptance can improve the company's category. A rejection of the agent's *different*
	// URL says nothing about the stored one, so it must not relabel a company "wrong" on the
	// strength of a page it never claimed to be.
	if agentVerdict.Accepted() {
		out.category = CategoryConfirmed
	}
	return out
}

// gate applies the shared validation gate to a browser result.
//
// This is the M3 requirement made structural: the browser tier has no rules of its own. The result
// is mapped into the same careers.Response a plain HTTP fetch produces, and careers.Validate
// decides. A page that refused, timed out, or never loaded becomes a transport error - an unknown -
// rather than a rejection, so a blocked host is never recorded as a wrong URL.
func (r Runner) gate(c Company, url, source string, res browseruse.Result) careers.Verdict {
	return careers.Validate(careers.Candidate{
		URL:         url,
		Kind:        kindFor(url),
		CompanyName: c.Name,
		Source:      source,
		// The validation profile. The generous rules are safe when a tier has already looked for a
		// careers page; here the URL may be anything, and "the path contains job" accepted a
		// stock-quote page for the ticker JOB during the first sweep.
		RequireJobListingEvidence: true,
	}, browseruseResponse(res))
}

// browseruseResponse maps a worker result onto the fetch response the gate consumes.
func browseruseResponse(res browseruse.Result) careers.Response {
	if !res.OK {
		return careers.Response{
			FinalURL:       res.FinalURL,
			TransportError: errors.New(res.Error),
			TimedOut:       res.ErrorKind == browseruse.KindTimeout,
		}
	}
	return careers.Response{
		FinalURL:    res.FinalURL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Body:        res.Body,
		Truncated:   res.Truncated,
	}
}

// kindFor decides which rule set the gate applies.
//
// The plan's rule is that a candidate's Kind follows where the URL points, not which tier produced
// it: a stored careers URL that is really a Greenhouse board is validated as a board, where the
// title is the whole of the evidence. Only the three vendors with a verified public API are
// recognised here; an unrecognised host is a first-party careers page as far as the gate is
// concerned, which is the more permissive rule and the right default for a URL we already trusted.
func kindFor(rawURL string) careers.Kind {
	if _, _, err := careers.ParseTenant(rawURL); err == nil {
		return careers.KindATSBoard
	}
	return careers.KindCareerSite
}

// attemptFor builds the audit-trail row for one fetch.
//
// The response body is retained only for a non-accepted attempt. An accepted page's evidence is its
// verdict, status and title; keeping 600 confirmed homepages on disk would be six hundred megabytes
// of files nobody opens, while the eighty-odd rejections are the ones a human actually reads.
func attemptFor(c Company, source, url string, res browseruse.Result, v careers.Verdict, evidenceBytes int64) store.ResolutionAttempt {
	// An escalation's whole value is the reasoning behind where it went, and that reasoning exists
	// nowhere else - the second render only sees the URL it was handed. Without this, the trail for
	// an escalated company says "a different URL was tried" and nothing about why.
	evidence := v.Evidence
	if note := strings.TrimSpace(res.Note); note != "" {
		evidence = appendEvidence(evidence, "note="+truncate(note, noteEvidenceLimit))
	}

	a := store.ResolutionAttempt{
		Source:          source,
		CandidateURL:    url,
		Kind:            kindFor(url),
		HTTPStatus:      res.Status,
		FinalURL:        v.FinalURL,
		Title:           v.Title,
		ValidationState: v.Status,
		Reason:          v.Reason,
		Evidence:        evidence,
		ContentType:     res.ContentType,
	}
	if v.Accepted() {
		// The schema enforces that an accepted attempt carries no rejection reason, and the store
		// refuses the write otherwise. The accepted verdict is still worth keeping - html_careers and
		// ats_board are different kinds of acceptance - so it moves into the evidence field rather
		// than being dropped.
		a.Reason = ""
		a.Evidence = withVerdict(v.Reason, evidence)
	}
	// Evidence retention. A rejected attempt keeps everything the gate saw, because that is the
	// record a human reads when they want to know why. An accepted attempt keeps a bounded prefix,
	// because the only thing it is ever needed for is re-judging the page under changed rules - and
	// the gate reads from the front of the document. Without this, a rule change costs a full
	// re-fetch of every URL, which is exactly what the first sweep's wrong "confirmed" verdicts made
	// us pay.
	if len(res.Body) > 0 {
		switch {
		case !v.Accepted():
			a.Body = res.Body
		case evidenceBytes > 0:
			body := res.Body
			if int64(len(body)) > evidenceBytes {
				body = body[:evidenceBytes]
			}
			a.Body = body
		}
	}
	return a
}

// noteEvidenceLimit bounds an agent's reasoning in the attempts row. The column is meant to be
// queryable, not to hold an essay; the full text is already on stderr in the run log.
const noteEvidenceLimit = 500

// appendEvidence joins evidence fragments, skipping empties so a separator never dangles.
func appendEvidence(existing, item string) string {
	switch {
	case strings.TrimSpace(item) == "":
		return existing
	case strings.TrimSpace(existing) == "":
		return item
	default:
		return existing + ";" + item
	}
}

// withVerdict prefixes a verdict label onto the free-form evidence summary.
func withVerdict(reason careers.Reason, evidence string) string {
	return appendEvidence("verdict="+string(reason), evidence)
}

// truncate shortens s to at most n bytes on a rune boundary, marking that it was cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "..."
}

// classify maps a verdict to the company-level category.
//
// The default is unverifiable, and that direction is deliberate. A reason this function has not
// been taught is a gap in the classifier, and the honest response to "I do not know what this
// verdict means" is to call the URL unconfirmed - never to accuse a company's careers page of being
// wrong. TestEveryDeclaredReasonIsClassified closes the gap from the other side.
func classify(v careers.Verdict) Category {
	if v.Accepted() {
		return CategoryConfirmed
	}
	if contentReasons[v.Reason] {
		return CategoryWrong
	}
	return CategoryUnverifiable
}

// contentReasons are the rejections that mean "a page was received and it is not a careers page".
// Every other rejection, and every error, leaves the URL unproven rather than disproven.
//
// empty_body is deliberately absent. A 2xx with nothing in it is the absence of evidence, not
// evidence of a wrong page - it is what a JavaScript application shell looks like before it paints,
// and calling it "wrong" would accuse a URL of being a product page on the strength of a body we
// never read. It stays in the escalate set below, because a navigating agent is exactly the remedy.
var contentReasons = map[careers.Reason]bool{
	careers.OutcomeGenericTitleWithoutCompany: true,
	careers.OutcomeUnverifiableATSTitle:       true,
	careers.OutcomeProductOrInvestorPath:      true,
	careers.OutcomeLocaleOnlyPath:             true,
	careers.OutcomeNoCareersSignal:            true,
	careers.OutcomeParkedDomain:               true,
	careers.OutcomeATSEmptyBoard:              true,
	careers.OutcomeATSTruncatedBody:           true,
	careers.OutcomeATSInvalidJSON:             true,
}

// escalateReasons are the verdicts where the page itself never arrived, so a second attempt driven
// by an agent can change the answer. A content verdict is the page's answer and is never escalated:
// no amount of navigation makes a product page into a careers page.
//
// Rate limiting is excluded on purpose - retrying a host that just asked us to slow down is exactly
// the behaviour that gets the whole sweep blocked - and so is an unparseable redirect target, which
// is a malformed stored URL rather than a page we failed to read.
var escalateReasons = map[careers.Reason]bool{
	careers.OutcomeBotChallenge:   true,
	careers.OutcomeForbidden:      true,
	careers.OutcomeTransportError: true,
	careers.OutcomeTimeout:        true,
	careers.OutcomeNoStatus:       true,
	careers.OutcomeEmptyBody:      true,
}

func shouldEscalate(r careers.Reason) bool { return escalateReasons[r] }

// sameURL reports whether two URLs name the same page, ignoring the cosmetic differences the gate
// already normalises away.
func sameURL(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	return careers.NormaliseURL(a) == careers.NormaliseURL(b)
}

// StoredFilter narrows which stored URLs a run considers. The narrowing happens in SQL so a
// restricted run does not read the whole table.
type StoredFilter struct {
	// OnlySlugs restricts the run to these slugs. Empty means no restriction.
	OnlySlugs []string
	// SkipValidated is the default rule: skip a company whose verdict was reached about the URL it
	// stores now. It is deliberately not "skip anything with a verdict" - a verdict about a
	// different URL is stale, and a resolver that changed the URL between runs is exactly the case
	// that makes the distinction matter. See migration 011.
	SkipValidated bool
	// VerdictCutoff is an RFC3339 UTC timestamp. Companies with no current verdict, or one older
	// than this, are considered; companies validated at or after it are skipped. Empty means no age
	// restriction. It is a string rather than a time.Time because it is compared against a stored
	// text column, and formatting it once here keeps the two in step.
	VerdictCutoff string
}

// notCurrentVerdictSQL is true when the company has no verdict, or a verdict that was reached about
// a different URL than the one stored now. Both cases mean "not validated" for selection purposes.
const notCurrentVerdictSQL = `(career_site_url_verdict IS NULL
       OR career_site_url_verdict_url IS NULL
       OR career_site_url_verdict_url <> career_site_url)`

// VerdictTimestampLayout is the format SQLite's strftime('%Y-%m-%dT%H:%M:%fZ','now') produces. The
// stale-after comparison is lexical, which is only correct because every value in the column has
// this exact fixed width and UTC offset.
const VerdictTimestampLayout = "2006-01-02T15:04:05.000Z"

// VerdictCutoff returns the timestamp before which a verdict counts as stale.
func VerdictCutoff(now time.Time, age time.Duration) string {
	return now.Add(-age).UTC().Format(VerdictTimestampLayout)
}

// storedURLQuery is the set of companies a validation run can consider.
//
// It is a different question from the resolution run's: that one wants companies needing a URL, this
// one wants companies that have one. Reading an empty-string value as absent matters - the column is
// nullable and older rows carry "" as well as NULL.
const storedURLBase = `
SELECT id, slug, name, career_site_url
  FROM companies
 WHERE career_site_url IS NOT NULL AND trim(career_site_url) <> ''`

// LoadStoredCompanies reads the companies whose careers URL can be validated, applying the filter in
// SQL.
func LoadStoredCompanies(ctx context.Context, db *sql.DB, filter StoredFilter) ([]Company, error) {
	query := storedURLBase
	args := []any{}

	if len(filter.OnlySlugs) > 0 {
		placeholders := make([]string, 0, len(filter.OnlySlugs))
		for _, slug := range filter.OnlySlugs {
			placeholders = append(placeholders, "?")
			args = append(args, strings.TrimSpace(slug))
		}
		query += ` AND slug IN (` + strings.Join(placeholders, ",") + `)`
	}
	if filter.SkipValidated {
		query += ` AND ` + notCurrentVerdictSQL
	}
	if filter.VerdictCutoff != "" {
		// A stale verdict, an absent one, one about another URL, or one with no timestamp all mean
		// "consider this company"; only a fresh verdict about the current URL is skipped. The
		// timestamp test is spelled out rather than left to `at < ?` because a NULL there would
		// compare to NULL and silently drop the row out of both sets.
		query += ` AND (` + notCurrentVerdictSQL + `
        OR career_site_url_verdict_at IS NULL
        OR career_site_url_verdict_at < ?)`
		args = append(args, filter.VerdictCutoff)
	}
	query += ` ORDER BY slug`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load companies with a stored careers URL: %w", err)
	}
	defer rows.Close()

	var out []Company
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.CareerSiteURL); err != nil {
			return nil, fmt.Errorf("scan company: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate companies: %w", err)
	}
	return out, nil
}
