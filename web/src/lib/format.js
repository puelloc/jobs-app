/**
 * Presentation helpers. Pure functions with no side effects, no fetching, and
 * no reactive state: the API sends raw column values and every human-facing
 * string is built here.
 */

/**
 * UTC wall-clock of an RFC3339 timestamp, e.g. "2026-09-27 01:51:43". Slicing
 * the string keeps the server-rendered date identical to the client-rendered
 * one; passing the instant through a local-timezone Date would not.
 * @param {string|null|undefined} isoString
 * @returns {string}
 */
export function formatUtc(isoString) {
	if (!isoString) return '—';
	const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2})/.exec(isoString);
	return m ? `${m[1]} ${m[2]}` : isoString;
}

/**
 * Human label for a run status. The text always carries the meaning; colour is
 * only ever an addition to it.
 * @param {string|null|undefined} status
 * @returns {string}
 */
export function formatRunStatus(status) {
	switch (status) {
		case 'running':
			return 'Running';
		case 'ok':
			return 'OK';
		case 'error':
			return 'Error';
		default:
			return status ? status.charAt(0).toUpperCase() + status.slice(1) : 'Unknown';
	}
}

/**
 * Wall-clock duration between two RFC3339 instants, e.g. "1m 12s". Empty when
 * either end is missing - an in-flight run has no duration yet.
 * @param {string|null|undefined} startIso
 * @param {string|null|undefined} finishIso
 * @returns {string}
 */
export function formatDuration(startIso, finishIso) {
	if (!startIso || !finishIso) return '';
	const ms = Date.parse(finishIso) - Date.parse(startIso);
	if (Number.isNaN(ms) || ms < 0) return '';

	const totalSeconds = Math.round(ms / 1000);
	const s = totalSeconds % 60;
	const m = Math.floor(totalSeconds / 60) % 60;
	const h = Math.floor(totalSeconds / 3600);
	if (h > 0) return `${h}h ${m}m ${s}s`;
	if (m > 0) return `${m}m ${s}s`;
	return `${s}s`;
}

/**
 * The one label a nullable field renders when its value is missing and the field
 * is still shown (detail views). Nullable fields in lists are omitted instead.
 */
export const NOT_STATED = 'Not stated';

/**
 * A snake_case token as a human label, e.g. "no_careers_signal" -> "No Careers
 * Signal", "ats_http_4xx" -> "ATS HTTP 4xx". Shared by employment_type,
 * candidate_kind and rejection_reason, which all reuse the same closed-vocabulary
 * spelling.
 * @param {string|null|undefined} value
 * @returns {string}
 */
export function humanize(value) {
	if (!value) return '';
	const acronyms = new Set(['http', 'ats', 'api', 'url', 'html', 'dns', 'tls', 'ssl']);
	return value
		.split('_')
		.map((word) => (acronyms.has(word.toLowerCase()) ? word.toUpperCase() : word.charAt(0).toUpperCase() + word.slice(1)))
		.join(' ');
}

/**
 * Human label for a company's careers-URL validation verdict. A null verdict
 * means "no validation has judged the current URL yet", which is a real state of
 * its own rather than "not known": the label says so.
 * @param {string|null|undefined} verdict
 * @returns {string}
 */
export function formatVerdict(verdict) {
	switch (verdict) {
		case 'confirmed':
			return 'Confirmed';
		case 'wrong':
			return 'Wrong';
		case 'unverifiable':
			return 'Unverifiable';
		default:
			return 'Not validated';
	}
}

/**
 * Human label for a job's status. Text always carries the meaning; colour is an
 * addition on top of it.
 * @param {string|null|undefined} status
 * @returns {string}
 */
export function formatJobStatus(status) {
	switch (status) {
		case 'open':
			return 'Open';
		case 'closed':
			return 'Closed';
		case 'filled':
			return 'Filled';
		case 'unknown':
			return 'Unknown';
		default:
			return status ? humanize(status) : 'Unknown';
	}
}

/**
 * A resolution-attempt count as a noun phrase, e.g. "0 attempts", "1 attempt".
 * @param {number|null|undefined} count
 * @returns {string}
 */
export function formatAttemptCount(count) {
	const n = count ?? 0;
	return n === 1 ? '1 attempt' : `${n} attempts`;
}

/**
 * Human label for an index membership value, e.g. "sp500" -> "S&P 500".
 * @param {string|null|undefined} value
 * @returns {string}
 */
export function formatIndexMembership(value) {
	switch (value) {
		case 'sp500':
			return 'S&P 500';
		case 'sp400':
			return 'S&P 400';
		case 'sp600':
			return 'S&P 600';
		default:
			return value ?? '';
	}
}

/**
 * Human label for a resolution attempt's validation status.
 * @param {string|null|undefined} status
 * @returns {string}
 */
export function formatValidationStatus(status) {
	switch (status) {
		case 'accepted':
			return 'Accepted';
		case 'rejected':
			return 'Rejected';
		case 'error':
			return 'Error';
		default:
			return status ? humanize(status) : 'Unknown';
	}
}

/**
 * One browser-use agent action as a readable line. An action is a single-key
 * object like { navigate: { url: "https://…" } }; this prefers the action name
 * plus the url/text it carries, falling back to compact JSON for anything else.
 * @param {any} action
 * @returns {string}
 */
export function formatTraceAction(action) {
	if (action === null || action === undefined) return '';
	if (typeof action !== 'object') return String(action);

	const entries = Object.entries(action);
	if (entries.length === 1) {
		const [name, params] = entries[0];
		if (params && typeof params === 'object' && !Array.isArray(params)) {
			const url = params.url ?? params.href ?? '';
			const text = params.text ?? '';
			if (url) return `${name} ${url}`;
			if (text) return `${name} ${text}`;
		}
		if (params === null || params === undefined) return name;
		if (typeof params === 'string' || typeof params === 'number' || typeof params === 'boolean') {
			return `${name} ${params}`;
		}
	}
	try {
		return JSON.stringify(action);
	} catch {
		return String(action);
	}
}

/**
 * Human label for a job's location: the free-text location, else the country,
 * else "Remote" when the job is remote, else empty (the segment is dropped).
 * @param {{ location_text?: string|null, country?: string|null, is_remote?: boolean }} job
 * @returns {string}
 */
export function formatLocation(job) {
	if (job?.location_text) return job.location_text;
	if (job?.country) return job.country;
	if (job?.is_remote) return 'Remote';
	return '';
}

/**
 * A compensation line from the four salary columns, e.g. "$120,000–$160,000 / yr".
 * Empty when both amounts are null (never "$0" or a fabricated range). A null
 * currency renders no symbol and a null period renders no "/ unit" suffix.
 * @param {{ min_cents?: number|null, max_cents?: number|null, currency?: string|null, period?: string|null }|null|undefined} salary
 * @returns {string}
 */
export function formatSalary(salary) {
	if (!salary) return '';
	const min = formatCents(salary.min_cents);
	const max = formatCents(salary.max_cents);
	if (min === null && max === null) return '';

	const symbol = salary.currency ? currencySymbol(salary.currency) : '';
	let range;
	if (min !== null && max !== null) {
		range = min === max ? `${symbol}${min}` : `${symbol}${min}–${symbol}${max}`;
	} else if (min !== null) {
		range = `from ${symbol}${min}`;
	} else {
		range = `up to ${symbol}${max}`;
	}
	if (salary.period) range += ` / ${formatPeriod(salary.period)}`;
	return range;
}

/**
 * Minor units to a grouped decimal string: 5500000 -> "55,000", 3000 -> "30".
 * The grouping is done by hand so the server-rendered string is byte-identical
 * to the client-rendered one (no locale-dependent Intl output).
 * @param {number|null|undefined} cents
 * @returns {string|null}
 */
function formatCents(cents) {
	if (cents === null || cents === undefined) return null;
	const sign = cents < 0 ? '-' : '';
	const abs = Math.abs(cents);
	const whole = Math.floor(abs / 100);
	const fraction = abs % 100;
	const grouped = String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
	const amount = fraction === 0 ? grouped : `${grouped}.${String(fraction).padStart(2, '0')}`;
	return `${sign}${amount}`;
}

/**
 * A currency's symbol, or the code plus a space when the code is not one this
 * viewer has a symbol for. Null currency is handled by the caller (no symbol).
 * @param {string} code
 * @returns {string}
 */
function currencySymbol(code) {
	const symbols = {
		USD: '$',
		EUR: '€',
		GBP: '£',
		JPY: '¥',
		CNY: '¥',
		INR: '₹',
		KRW: '₩',
		CAD: 'CA$',
		AUD: 'A$',
		NZD: 'NZ$',
		SGD: 'S$',
		HKD: 'HK$',
		BRL: 'R$',
		MXN: 'MX$',
		CHF: 'CHF ',
		SEK: 'SEK ',
		NOK: 'NOK ',
		DKK: 'DKK ',
		PLN: 'PLN '
	};
	return symbols[code] ?? `${code} `;
}

/**
 * A salary period as its "/ unit" suffix.
 * @param {string|null|undefined} period
 * @returns {string}
 */
function formatPeriod(period) {
	switch (period) {
		case 'year':
			return 'yr';
		case 'month':
			return 'mo';
		case 'hour':
			return 'hr';
		default:
			return period ?? '';
	}
}
