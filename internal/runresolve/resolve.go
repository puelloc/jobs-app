// resolve.go: the per-company resolution and the batch homepage lookup.
//
// Split from runresolve.go so the run loop stays readable: that file is about which companies and
// what counts, this one is about what happens to a single company.
package runresolve

import (
	"context"
	"fmt"

	"jobsapp/internal/careers"
	"jobsapp/internal/careers/homepage"
	"jobsapp/internal/store"
)

// resolveHomepages runs tier 1 once for every article in the batch.
//
// Batched because it can be: the APIs take 50 titles per call, so the whole S&P 1500 costs roughly
// 30 requests per source rather than one per company. A per-company lookup here would turn the
// cheapest tier into the most expensive one.
//
// A company whose title the sources do not know is simply absent from the result, which is not an
// error: tier 1 is an enrichment, and the ladder still runs for a company that already has a website.
func (r Runner) resolveHomepages(ctx context.Context, client *homepage.Client, works []Company) map[string]homepage.Result {
	titles := make([]string, 0, len(works))
	seen := map[string]bool{}
	for _, c := range works {
		if c.Article == "" || seen[c.Article] {
			continue
		}
		seen[c.Article] = true
		titles = append(titles, c.Article)
	}
	if len(titles) == 0 {
		return nil
	}

	got, err := client.Resolve(ctx, homepage.SortedTitles(titles))
	if err != nil {
		// Tier 1 failing is not fatal: every company that already has a website still resolves, and
		// the rest are left for a later pass rather than being marked unresolved on a network blip.
		return nil
	}
	return got
}

// resolveOne resolves a single company into a persistable Resolution.
func (r Runner) resolveOne(
	ctx context.Context,
	client *homepage.Client,
	c Company,
	home homepage.Result,
	runID int64,
	opts Options,
) (store.Resolution, error) {
	res := store.Resolution{
		CompanyID:   c.ID,
		CompanySlug: c.Slug,
		DryRun:      opts.DryRun,
	}

	// The website is the ladder's starting point. An existing one wins, because it was resolved by
	// an earlier pass and tier 1 is only a fallback for companies that lack one.
	website := c.Website
	if website == "" && home.URL != "" {
		website = home.URL
		res.Website = home.URL
		res.WebsiteSource = websiteSource(home.Source)
		res.Attempts = append(res.Attempts, store.ResolutionAttempt{
			Source:          string(home.Source),
			CandidateURL:    home.URL,
			Kind:            careers.KindWebsite,
			HTTPStatus:      200,
			FinalURL:        home.URL,
			ValidationState: careers.StatusAccepted,
		})
	}

	if website == "" {
		// Without a homepage there is nothing to scan, and this is a legitimate unresolved outcome
		// rather than a failure: the plan routes these ~252 no-article companies to the SEC 10-K
		// tier, which is deferred.
		res.Attempts = append(res.Attempts, store.ResolutionAttempt{
			Source:          "tier1",
			CandidateURL:    "",
			Kind:            careers.KindWebsite,
			ValidationState: careers.StatusRejected,
			Reason:          careers.OutcomeNoCareersSignal,
		})
		return res, nil
	}

	ladder := careers.Resolver{Fetcher: r.Fetcher}
	outcome, err := ladder.Resolve(ctx, c.Name, website)
	if err != nil {
		return res, fmt.Errorf("resolve %s: %w", c.Slug, err)
	}
	res.Attempts = append(res.Attempts, ladderAttempts(outcome, website)...)

	if !outcome.CareerSiteURLIsSet() {
		return res, nil
	}

	res.CareerSiteURL = outcome.CareerSiteURL
	res.CareerSiteSource = careerSiteSource(outcome.Source)
	res.CareerSiteStatus = statusOf(outcome)
	res.CareerSiteTitle = outcome.Title

	// Tier 5a: when the accepted URL landed on a known board, ask the vendor's API whether the board
	// is real. The validation gate already judged the page; this is the stricter check that an empty
	// board cannot pass, and it is where the ATS identity is established.
	if host := atsHostOf(outcome); host != "" {
		ats := careers.ATSResolver{Fetcher: r.Fetcher}
		board, err := ats.Resolve(ctx, outcome.CareerSiteURL)
		if err == nil {
			attempt := store.ResolutionAttempt{
				Source:       "ats_api",
				CandidateURL: board.APIURL,
				Kind:         careers.KindATSBoard,
				HTTPStatus:   board.HTTPStatus,
				FinalURL:     board.TenantURL,
				Evidence:     board.Evidence,
			}
			if board.Accepted {
				attempt.ValidationState = careers.StatusAccepted
				// The tenant URL is the human-facing board, and it becomes the stored value. The
				// API URL stays in the attempt row, so the trail still says what was read.
				res.CareerSiteURL = board.TenantURL
				res.CareerSiteSource = store.CareerSiteSourceATSAPI
				res.ATSPlatformID = platformIDFor(host)
				res.ATSBaseURL = board.TenantURL
			} else {
				attempt.ValidationState = rejectionState(board.Reason)
				attempt.Reason = board.Reason
				// The board failed its API check, so the ladder's answer stands only if it was a
				// branded page rather than the board itself. Accepting the board URL here would
				// store a board we just proved we cannot read.
				if careers.SameHostAs(outcome.CareerSiteURL, outcome.ATSHost) {
					res.CareerSiteURL = ""
					res.CareerSiteSource = ""
					res.CareerSiteStatus = 0
					res.CareerSiteTitle = ""
				}
			}
			res.Attempts = append(res.Attempts, attempt)
		}
	}

	return res, nil
}

// ladderAttempts converts the ladder's attempts into persistable ones, carrying the response body so
// the caller can retain it as evidence.
func ladderAttempts(outcome careers.Outcome, homepageURL string) []store.ResolutionAttempt {
	out := make([]store.ResolutionAttempt, 0, len(outcome.Attempts))
	for _, a := range outcome.Attempts {
		item := store.ResolutionAttempt{
			Source:          a.Source,
			CandidateURL:    a.CandidateURL,
			Kind:            a.Kind,
			HTTPStatus:      a.HTTPStatus,
			FinalURL:        a.FinalURL,
			Title:           a.Title,
			ValidationState: a.ValidationState,
			Evidence:        a.Evidence,
			Body:            a.Body,
			ContentType:     a.ContentType,
		}
		// The gate carries a Reason on every verdict, including acceptance - there it names the
		// evidence that justified accepting. The attempt table's CHECK requires a non-accepted row
		// to have a reason and an accepted row to have none, so acceptance's reason is dropped here
		// rather than being copied into a column that must be NULL.
		if a.ValidationState != careers.StatusAccepted {
			item.Reason = a.RejectionReason
		}
		if item.CandidateURL == "" {
			item.CandidateURL = homepageURL
		}
		out = append(out, item)
	}
	return out
}

// statusOf returns the HTTP status behind an accepted outcome, or zero when it is not recorded.
//
// The ladder already followed redirects, so the accepted candidate's status is a 2xx; the value is
// taken from the attempt record rather than assumed, so a stored 200 always corresponds to a fetch
// that actually returned one.
func statusOf(outcome careers.Outcome) int {
	for _, a := range outcome.Attempts {
		if a.ValidationState == careers.StatusAccepted && a.CandidateURL == outcome.CareerSiteURL {
			return a.HTTPStatus
		}
	}
	for _, a := range outcome.Attempts {
		if a.ValidationState == careers.StatusAccepted {
			return a.HTTPStatus
		}
	}
	return 0
}

// atsHostOf returns the vendor host behind an accepted outcome, or empty when the answer is not a
// third-party board.
func atsHostOf(outcome careers.Outcome) string {
	if outcome.ATSHost != "" {
		return outcome.ATSHost
	}
	return ""
}

// platformIDFor maps a vendor host to its platforms row. The ids are the seeds from migration 002
// and 007; an unknown host yields zero, which the writer treats as "no platform to record".
func platformIDFor(host string) int64 {
	switch {
	case containsHost(host, "greenhouse.io"):
		return 10
	case containsHost(host, "lever.co"):
		return 11
	case containsHost(host, "myworkdayjobs.com"):
		return 12
	case containsHost(host, "icims.com"):
		return 13
	case containsHost(host, "ashbyhq.com"):
		return 14
	case containsHost(host, "smartrecruiters.com"):
		return 15
	default:
		return 0
	}
}

func containsHost(host, suffix string) bool {
	return host == suffix || len(host) > len(suffix) && host[len(host)-len(suffix)-1:] == "."+suffix
}

// websiteSource maps a tier-1 source to the stored vocabulary.
func websiteSource(s homepage.Source) store.WebsiteSource {
	switch s {
	case homepage.SourceInfobox:
		return store.WebsiteSourceWikipediaInfobox
	case homepage.SourceP856:
		return store.WebsiteSourceWikidataP856
	default:
		return ""
	}
}

// careerSiteSource maps a ladder tier name to the stored vocabulary.
func careerSiteSource(source string) store.CareerSiteSource {
	switch source {
	case "nav_anchor":
		return store.CareerSiteSourceAnchorScan
	case "sitemap":
		return store.CareerSiteSourceSitemap
	case "robots_path":
		return store.CareerSiteSourceRobotsSignal
	case "common_path":
		return store.CareerSiteSourceCommonPath
	default:
		// An unrecognised tier name is a programming error rather than data. Returning empty makes
		// the writer refuse the resolution with a message naming the company, which is better than
		// inventing a Source value that no tier produced.
		return ""
	}
}

// rejectionState maps a non-accepted reason to the attempt's validation_status.
func rejectionState(reason careers.Reason) careers.ValidationStatus {
	switch reason {
	case careers.OutcomeTransportError, careers.OutcomeTimeout, careers.OutcomeNoStatus:
		// An unknown, not a rejection: the site was never reached, so nothing is disproven.
		return careers.StatusError
	default:
		return careers.StatusRejected
	}
}
