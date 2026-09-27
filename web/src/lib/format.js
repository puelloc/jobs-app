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
