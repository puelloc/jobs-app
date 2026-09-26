import adapter from '@sveltejs/adapter-auto';

/**
 * Runes mode is forced on for every component, so any legacy syntax (on:click,
 * export let, $:, <slot>) is a compile error rather than a silent fallback.
 *
 * adapter-auto is SvelteKit's zero-configuration default: it chooses a target at
 * build time from the environment. No deployment target is configured here, and
 * development (the only supported mode for now) does not use it at all.
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
