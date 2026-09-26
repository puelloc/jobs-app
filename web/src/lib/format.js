/**
 * Presentation helpers. Pure functions with no side effects, no fetching, and
 * no reactive state: the API sends raw column values and every human-facing
 * string is built here (docs/ui-design.md, "API <-> UI boundaries").
 */

/** Symbols for the currencies most likely to appear; anything else renders as
 * its ISO code. A null currency renders no symbol at all. */
const CURRENCY_SYMBOLS = {
	USD: '$',
	EUR: '€',
	GBP: '£',
	CAD: 'CA$',
	AUD: 'A$',
	NZD: 'NZ$',
	JPY: '¥',
	INR: '₹',
	CHF: 'CHF ',
	SEK: 'kr ',
	BRL: 'R$'
};

const PERIOD_SUFFIXES = {
	year: ' / yr',
	month: ' / mo',
	hour: ' / hr'
};

/**
 * Render integer minor units (cents) as an amount. Whole amounts keep no
 * decimals; anything with cents shows two.
 * @param {number} cents
 * @returns {string}
 */
function formatAmount(cents) {
	const hasCents = cents % 100 !== 0;
	return (cents / 100).toLocaleString('en-US', {
		minimumFractionDigits: hasCents ? 2 : 0,
		maximumFractionDigits: hasCents ? 2 : 0
	});
}

/**
 * The compensation line. Both amount columns null means no amount is stated, so
 * the result is an empty string - never "$0" and never a placeholder.
 * @param {{ min_cents?: number|null, max_cents?: number|null, currency?: string|null, period?: string|null }|null|undefined} salary
 * @returns {string}
 */
export function formatSalary(salary) {
	if (!salary) return '';

	const min = salary.min_cents ?? null;
	const max = salary.max_cents ?? null;
	if (min === null && max === null) return '';

	const symbol = salary.currency ? (CURRENCY_SYMBOLS[salary.currency] ?? `${salary.currency} `) : '';
	const period = salary.period ? (PERIOD_SUFFIXES[salary.period] ?? '') : '';

	if (min !== null && max !== null) {
		return `${symbol}${formatAmount(min)}–${symbol}${formatAmount(max)}${period}`;
	}
	if (min !== null) {
		return `from ${symbol}${formatAmount(min)}${period}`;
	}
	return `up to ${symbol}${formatAmount(max)}${period}`;
}

/**
 * The UTC calendar date of an RFC3339 timestamp, or '' when absent. Slicing the
 * string keeps the server-rendered date identical to the client-rendered one;
 * passing the instant through a local-timezone Date would not.
 * @param {string|null|undefined} isoString
 * @returns {string}
 */
export function formatDate(isoString) {
	if (!isoString) return '';
	const date = isoString.slice(0, 10);
	return /^\d{4}-\d{2}-\d{2}$/.test(date) ? date : '';
}

/**
 * Whole days since an instant, in the "N days ago" shape the detail view uses
 * for last_seen_at. This is a freshness label for when a job was last observed,
 * not a statement about why its status is what it is: status is decided by
 * whether the latest scrape saw the job, never by elapsed time
 * (docs/scraper-design.md, "Freshness contract").
 * @param {string|null|undefined} isoString
 * @returns {string}
 */
export function formatRelative(isoString) {
	if (!isoString) return '';
	const then = Date.parse(isoString);
	if (Number.isNaN(then)) return '';

	const days = Math.floor((Date.now() - then) / 86_400_000);
	if (days <= 0) return 'today';
	if (days === 1) return '1 day ago';
	return `${days} days ago`;
}

/**
 * The location fallback chain: location_text, else country, else "Remote" when
 * the row is flagged remote, else nothing.
 * @param {{ location_text?: string|null, country?: string|null, is_remote?: boolean }|null|undefined} job
 * @returns {string}
 */
export function formatLocation(job) {
	if (!job) return '';
	if (job.location_text) return job.location_text;
	if (job.country) return job.country;
	if (job.is_remote) return 'Remote';
	return '';
}

/**
 * Human label for a status value. The text is always rendered; colour is only
 * ever an addition to it (docs/ui-design.md, list view).
 * @param {string|null|undefined} status
 * @returns {string}
 */
export function formatStatus(status) {
	switch (status) {
		case 'open':
			return 'Open';
		case 'closed':
			return 'Closed';
		case 'filled':
			return 'Filled';
		default:
			return 'Unknown';
	}
}

/**
 * Human label for the employment_type enum, which the API sends raw
 * ("full_time"). Absent stays an empty string so the caller can drop the whole
 * segment.
 * @param {string|null|undefined} employmentType
 * @returns {string}
 */
export function formatEmploymentType(employmentType) {
	if (!employmentType) return '';
	const spaced = employmentType.replace(/_/g, ' ');
	return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}
