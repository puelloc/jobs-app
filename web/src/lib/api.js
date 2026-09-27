/**
 * The only place this app talks to the Go API. Every read is a GET; the one
 * write is POST /api/companies/{id}/scrape, which the scrape button fires.
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
 * One request to the Go API, resolving with the parsed body or rejecting with
 * the { status, code, message } shape the routes turn into error pages.
 * @param {string} method
 * @param {string} url
 * @returns {Promise<any>}
 */
async function request(method, url) {
	let res;
	try {
		res = await fetch(url, { method, headers: { Accept: 'application/json' } });
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
 * GET one JSON endpoint.
 * @param {string} path
 * @param {{ origin?: string }} [options]
 * @returns {Promise<any>}
 */
async function getJSON(path, { origin = '' } = {}) {
	return request('GET', `${origin}${path}`);
}

/**
 * POST one JSON endpoint (the single write path). Client-side only: a scrape
 * trigger is a user action, never a server-render side effect.
 * @param {string} path
 * @returns {Promise<any>}
 */
async function postJSON(path) {
	return request('POST', path);
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

/**
 * One page of companies plus the envelope's total. `index`, `resolution` and
 * `search` are passed through verbatim: an empty value means "no filter".
 * @param {{ limit?: number, offset?: number, index?: string, resolution?: string, search?: string }} [params]
 * @param {{ origin?: string }} [options]
 * @returns {Promise<{ companies: any[], limit: number, offset: number, total: number }>}
 */
export async function getCompanies(
	{ limit = 25, offset = 0, index = '', resolution = '', search = '' } = {},
	options = {}
) {
	const query = new URLSearchParams({ limit: String(limit), offset: String(offset) });
	if (index) query.set('index', index);
	if (resolution) query.set('resolution', resolution);
	if (search) query.set('search', search);
	return getJSON(`/api/companies?${query.toString()}`, options);
}

/**
 * One company plus its whole resolution-attempt trail.
 * @param {number|string} id
 * @param {{ origin?: string }} [options]
 * @returns {Promise<{ company: any, attempts: any[] }>}
 */
export async function getCompany(id, options = {}) {
	return getJSON(`/api/companies/${id}`, options);
}

/**
 * One page of jobs plus the envelope's total.
 * @param {{ limit?: number, offset?: number }} [paging]
 * @param {{ origin?: string }} [options]
 * @returns {Promise<{ jobs: any[], limit: number, offset: number, total: number }>}
 */
export async function getJobs({ limit = 25, offset = 0 } = {}, options = {}) {
	return getJSON(`/api/jobs?limit=${limit}&offset=${offset}`, options);
}

/**
 * One job: the list item plus description and the three URLs.
 * @param {number|string} id
 * @param {{ origin?: string }} [options]
 * @returns {Promise<any>}
 */
export async function getJob(id, options = {}) {
	return getJSON(`/api/jobs/${id}`, options);
}

/**
 * Trigger one company's listings scrape. Rejects with the API's own message on
 * 400 (not classified) and 409 (a scrape is already running).
 * @param {number|string} id
 * @returns {Promise<{ run_id: number }>}
 */
export async function postScrape(id) {
	return postJSON(`/api/companies/${id}/scrape`);
}

/**
 * One run by id (the run detail page's header: status, counters, error text).
 * @param {number|string} id
 * @param {{ origin?: string }} [options]
 * @returns {Promise<any>}
 */
export async function getRun(id, options = {}) {
	return getJSON(`/api/runs/${id}`, options);
}

/**
 * The live agent trace for a run: the step/done events written so far. A run
 * with no trace yet (or a non-agent run) resolves with present=false.
 * @param {number|string} id
 * @param {{ origin?: string }} [options]
 * @returns {Promise<{ id: string, present: boolean, events: any[] }>}
 */
export async function getTrace(id, options = {}) {
	return getJSON(`/api/traces/${id}`, options);
}
