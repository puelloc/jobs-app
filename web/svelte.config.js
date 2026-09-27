import adapter from '@sveltejs/adapter-node';

/**
 * Runes mode is forced on for every component, so any legacy syntax (on:click,
 * export let, $:, <slot>) is a compile error rather than a silent fallback.
 *
 * adapter-node builds a standalone Node server (web/build/index.js) that runs in
 * the `ui` Docker service. `/api/*` requests are proxied to the Go API by
 * src/routes/api/[...path]/+server.js, so the browser only ever talks to this
 * one origin and no CORS configuration is needed anywhere.
 *
 * @type {import('@sveltejs/kit').Config}
 */
const config = {
	compilerOptions: {
		runes: true
	},
	kit: {
		adapter: adapter()
	}
};

export default config;
