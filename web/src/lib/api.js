/**
 * The only place this app talks to the Go API. Every call is a GET: the viewer
 * is read-only, so there is no write path here at all.
 *
 * No retries, no timeouts beyond the browser's own, and no reactive state -
 * load() functions import this module and nothing else does.
 *
 * `origin` is the origin the current page was requested with. In the browser a
 * relative URL would be enough, but there is nothing for the server render to be
 * relative to, and SvelteKit's own event.fetch routes a same-origin /api request
 * through this app's router, which has no /api route. Addressing the dev server
 * by its own origin keeps a single code path: the request lands on Vite, whose
 * /api proxy forwards it to the Go API.
 *
 * @typedef {{ status: number, code: string, message: string }} ApiFailure
 */

/**
 * GET one JSON endpoint and either resolve with the parsed body or reject with
 * the { status, code, message } shape the routes turn into error pages.
 * @param {string} path
 * @param {{ origin?: string }} [options]
 * @returns {Promise<any>}
 */
async function getJSON(path, { origin = '' } = {}) {
	let res;
	try {
		res = await fetch(`${origin}${path}`, { headers: { Accept: 'application/json' } });
	} catch {
		// The Go server is not running, or the proxy target refused the
		// connection. The page still has to render something honest.
		throw { status: 500, code: 'network_error', message: 'Could not reach the jobs API' };
	}

	if (!res.ok) {
		/** @type {{ error?: { code?: string, message?: string } }} */
		let envelope = {};
		try {
			envelope = await res.json();
		} catch {
			// A proxy-generated or otherwise non-JSON body: fall through to the
			// status-based message instead of surfacing a parse error.
		}
		const fallback =
			res.status >= 500
				? `The jobs API did not respond correctly (HTTP ${res.status})`
				: `Request failed (HTTP ${res.status})`;
		throw {
			status: res.status,
			code: envelope?.error?.code ?? 'unknown',
			message: envelope?.error?.message ?? fallback
		};
	}

	return await res.json();
}

/**
 * One page of runs, newest first, plus the envelope's total.
 * @param {{ limit?: number, offset?: number }} [paging]
 * @param {{ origin?: string }} [options]
 * @returns {Promise<{ runs: any[], limit: number, offset: number, total: number }>}
 */
export async function getRuns({ limit = 100, offset = 0 } = {}, options = {}) {
	return getJSON(`/api/runs?limit=${limit}&offset=${offset}`, options);
}
