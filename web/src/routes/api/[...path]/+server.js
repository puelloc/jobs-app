import { env } from '$env/dynamic/private';

/**
 * Server-side proxy for every /api/* request: forward it to the Go API and pass
 * the status and body straight through. The browser only ever talks to this one
 * origin, so no CORS configuration is needed anywhere and no component knows the
 * Go host.
 *
 * Used in production (adapter-node). In development, vite.config.js proxies /api
 * to the Go API before SvelteKit ever sees the request, so this route is inert
 * there.
 */

const DEFAULT_API = 'http://127.0.0.1:8080';

/** Base URL of the Go API, trailing slash trimmed. */
function apiBase() {
	return (env.JOBS_API ?? DEFAULT_API).replace(/\/+$/, '');
}

/**
 * @param {Request} request
 * @param {Record<string, string>} params
 * @param {URL} url
 */
async function proxy(request, params, url) {
	const target = `${apiBase()}/api/${params.path}${url.search}`;
	/** @type {RequestInit} */
	const init = {
		method: request.method,
		headers: { Accept: 'application/json' }
	};
	if (request.method !== 'GET' && request.method !== 'HEAD') {
		init.headers['content-type'] = request.headers.get('content-type') ?? 'application/json';
		init.body = await request.text();
	}

	const upstream = await fetch(target, init);
	const body = await upstream.arrayBuffer();
	return new Response(body, {
		status: upstream.status,
		headers: { 'content-type': upstream.headers.get('content-type') ?? 'application/json' }
	});
}

/** @type {import('./$types').RequestHandler} */
export async function GET({ request, params, url }) {
	return proxy(request, params, url);
}

/** @type {import('./$types').RequestHandler} */
export async function POST({ request, params, url }) {
	return proxy(request, params, url);
}
