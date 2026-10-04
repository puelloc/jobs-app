/**
 * A page that was open when a deploy happened keeps running the JavaScript it loaded, so a click can
 * send a request in the shape the *previous* build used — a resume whose options were silently
 * dropped, for instance. Nothing server-side can fix a tab that is already open, but the server should
 * not make it worse: an ETag alone lets a browser reuse an HTML response heuristically, whereas
 * `no-cache` means "revalidate before you use it", which costs a 304 and makes a plain reload enough
 * to pick up a new build.
 *
 * Immutable bundles are left alone: adapter-node serves them with a long max-age and a new build
 * references new hashed filenames, so caching them is the thing that makes the reload fast.
 *
 * @type {import('@sveltejs/kit').Handle}
 */
export const handle = async ({ event, resolve }) => {
	const response = await resolve(event);
	const contentType = response.headers.get('content-type') ?? '';
	if (contentType.includes('text/html')) {
		response.headers.set('cache-control', 'no-cache, must-revalidate');
	}
	return response;
};

/**
 * Unexpected server errors are logged with the route, status and stack so
 * `docker compose logs ui` shows what failed; the client still only sees the
 * generic message.
 * @type {import('@sveltejs/kit').HandleServerError}
 */
export const handleError = ({ error, event, status, message }) => {
	console.error(`[ui] error ${status} on ${event.request.method} ${event.url.pathname}: ${message}`, error);
	return { message };
};
