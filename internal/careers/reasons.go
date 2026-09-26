// reasons.go: the controlled vocabulary for a resolution attempt's outcome.
//
// Every verdict carries exactly one Reason, and that value is persisted verbatim into
// url_resolution_attempts.rejection_reason. It is therefore an enum rather than free text: the
// plan's premise is that a bad pick is auditable after the fact, and an audit trail with drifting
// vocabulary is not one. "http_404" and "not_found" and "missing" are the same fact spelled three
// ways, and a query cannot count a fact with three names.
//
// Adding a value is a deliberate act: a Reason must have a code path that produces it and a test
// that provokes it. TestEveryReasonIsProducible enforces the second half.
//
// design: docs/sp1500-plan.md, section 7.2 and the M2a notes.
package careers

import "strconv"

// Reason labels why a candidate was accepted, rejected, or left unknown.
//
// These strings are stored in the database. Renaming one is a data migration, not a refactor.
type Reason string

const (
	// --- transport: the site was never reached, so nothing is proven ---

	// OutcomeTransportError means the request failed before a complete response: DNS, connection
	// refused or reset, TLS failure. An unknown, not a rejection, so the site stays retryable.
	OutcomeTransportError Reason = "transport_error"
	// OutcomeNoStatus means a response object reached the gate with no status set, which is a
	// defect in the fetcher rather than a fact about the site.
	OutcomeNoStatus Reason = "no_status"

	// --- HTTP status: an answer was received ---

	// OutcomeForbidden is a 403. Recorded rather than disguised: no User-Agent spoofing.
	OutcomeForbidden Reason = "forbidden"
	// OutcomeRateLimited is a 429. Distinct from forbidden because the remedy differs.
	OutcomeRateLimited Reason = "rate_limited"
	// OutcomeUnparseableFinalURL means the URL cannot be stored because it cannot be parsed, which
	// usually indicates a redirect to something that is not a web address.
	OutcomeUnparseableFinalURL Reason = "unparseable_final_url"
	// OutcomeEmptyBody means a 2xx arrived with nothing in it.
	OutcomeEmptyBody Reason = "empty_body"
	// OutcomeUnknownKind means the caller asked for a rule set that does not exist.
	OutcomeUnknownKind Reason = "unknown_kind"

	// --- content: an answer was received and it is not the page that was wanted ---

	// OutcomeBotChallenge means a bot wall answered 200 with a challenge page.
	OutcomeBotChallenge Reason = "bot_challenge"
	// OutcomeParkedDomain means a parking page answered 200. Observed on a stale Wikidata URL.
	OutcomeParkedDomain Reason = "parked_domain"
	// OutcomeGenericTitleWithoutCompany means the document title is a vendor default and does not
	// name the company, which is how Ashby and SmartRecruiters answer for a nonexistent company.
	OutcomeGenericTitleWithoutCompany Reason = "generic_title_without_company"
	// OutcomeUnverifiableATSTitle means the page is third-party hosted and the caller supplied no
	// usable company token, so the page cannot be corroborated at all. The absent-input case is its
	// own rejection rather than a vacuous pass.
	OutcomeUnverifiableATSTitle Reason = "unverifiable_ats_title"
	// OutcomeProductOrInvestorPath means the URL names a product, storefront, or investor-relations
	// page rather than a place to find work.
	OutcomeProductOrInvestorPath Reason = "product_or_investor_path"
	// OutcomeLocaleOnlyPath means every path segment is a language or region qualifier, so the URL
	// is a regional landing page rather than the site.
	OutcomeLocaleOnlyPath Reason = "locale_only_path"
	// OutcomeNoCareersSignal means the page is well-formed but carries no evidence of being a
	// careers page.
	OutcomeNoCareersSignal Reason = "no_careers_signal"
	// OutcomeATSBoardEmpty means a JSON job board was reached but lists nothing.
	OutcomeATSBoardEmpty Reason = "ats_board_empty"

	// --- accepted ---

	// OutcomeHTMLHomepage means a corporate homepage was accepted. It carries no careers evidence by
	// design: it is the stepping stone the careers tiers run against.
	OutcomeHTMLHomepage Reason = "html_homepage"
	// OutcomeHTMLCareers means a careers page was accepted on content evidence.
	OutcomeHTMLCareers Reason = "html_careers"
	// OutcomeATSBoard means a populated JSON job board was accepted.
	OutcomeATSBoard Reason = "ats_board"
)

// httpReason builds the Reason for an unexpected HTTP status. Any status can occur, so this cannot
// be a fixed enum member; it is centralised here so the spelling is consistent and the prefix is
// greppable, which is what lets a query group "everything that was an HTTP failure".
func httpReason(status int) Reason {
	return Reason("http_" + strconv.Itoa(status))
}

// AllReasons lists every Reason a verdict can carry, for tests and for reporting. The
// http_<status> family is represented by its common members; httpReason covers the rest.
func AllReasons() []Reason {
	return []Reason{
		OutcomeTransportError,
		OutcomeNoStatus,
		OutcomeForbidden,
		OutcomeRateLimited,
		OutcomeUnparseableFinalURL,
		OutcomeEmptyBody,
		OutcomeUnknownKind,
		OutcomeBotChallenge,
		OutcomeParkedDomain,
		OutcomeGenericTitleWithoutCompany,
		OutcomeUnverifiableATSTitle,
		OutcomeProductOrInvestorPath,
		OutcomeLocaleOnlyPath,
		OutcomeNoCareersSignal,
		OutcomeATSBoardEmpty,
		OutcomeHTMLHomepage,
		OutcomeHTMLCareers,
		OutcomeATSBoard,
		httpReason(404),
		httpReason(500),
		httpReason(503),
	}
}
