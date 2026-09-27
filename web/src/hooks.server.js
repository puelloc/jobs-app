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
