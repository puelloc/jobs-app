/**
 * The only place this app talks to the Go API. Every call is a GET: the viewer
 * is read-only, so there is no write path here at all.
 *
 * No retries, no timeouts beyond the browser's own, and no reactive state -
 * load() functions import this module and nothing else does.
 *
 * @typedef {{ status: number, code: string, message: string }} ApiFailure
 */

/**
 * GET one JSON endpoint and either resolve with the parsed body or reject with
 * the { status, code, message } shape the routes turn into error pages.
 *
 * `origin` is the origin the current page was requested with. In the browser a
 * relative URL would be enough, but there is nothing for the server render to be
 * relative to, and SvelteKit's own event.fetch routes a same-origin /api request
 * through this app's router, which has no /api route. Addressing the dev server
 * by its own origin keeps a single code path: the request lands on Vite, whose
 * /api proxy forwards it to the Go API.
 *
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
 * One page of jobs, most recent first, plus the envelope's total.
 * @param {{ limit?: number, offset?: number }} [paging]
 * @param {{ origin?: string }} [options]
 * @returns {Promise<{ jobs: any[], limit: number, offset: number, total: number }>}
 */
export async function getJobs({ limit = 25, offset = 0 } = {}, options = {}) {
	return getJSON(`/api/jobs?limit=${limit}&offset=${offset}`, options);
}

/**
 * One job, including description and the three URLs.
 * @param {string|number} id
 * @param {{ origin?: string }} [options]
 * @returns {Promise<any>}
 */
export async function getJob(id, options = {}) {
	return getJSON(`/api/jobs/${encodeURIComponent(id)}`, options);
}

/**
 * One page of companies, with the directory filters the API accepts.
 *
 * Empty filter values are dropped rather than sent as empty strings: the API
 * treats an absent parameter as "no filter", and sending `resolution=` empty
 * would be indistinguishable from a value it does not recognise, which it
 * rejects with a 400.
 *
 * @param {{ index?: string, resolution?: string, search?: string, limit?: number, offset?: number }} [filter]
 * @param {{ origin?: string }} [options]
 * @returns {Promise<{ companies: any[], limit: number, offset: number, total: number }>}
 */
export async function getCompanies(
	{ index = '', resolution = '', search = '', limit = 25, offset = 0 } = {},
	options = {}
) {
	const params = new URLSearchParams();
	params.set('limit', String(limit));
	params.set('offset', String(offset));
	if (index) params.set('index', index);
	if (resolution) params.set('resolution', resolution);
	if (search) params.set('search', search);
	return getJSON(`/api/companies?${params}`, options);
}

/**
 * One company plus its whole resolution trail.
 * @param {string|number} id
 * @param {{ origin?: string }} [options]
 * @returns {Promise<any>}
 */
export async function getCompany(id, options = {}) {
	return getJSON(`/api/companies/${encodeURIComponent(id)}`, options);
}
