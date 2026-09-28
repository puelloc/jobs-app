import { error } from '@sveltejs/kit';
import { getRuns, getSweepPosition } from '$lib/api.js';

// Server-rendered: this runs during SSR so the first HTML response already
// contains the runs. The page then re-runs it on an interval via invalidate to
// pick up live status.
/** @type {import('./$types').PageLoad} */
export async function load({ fetch, depends }) {
	depends('data:runs');

	let data;
	try {
		data = await getRuns({ limit: 100, offset: 0 }, { fetch });
	} catch (failure) {
		// The API's own message when it sent an error envelope; a readable
		// fallback when the failure never reached the API at all.
		error(failure?.status ?? 500, failure?.message ?? 'Could not load runs');
	}

	// The resume point is best-effort: a failure to read it must not break the page.
	let sweep = { present: false, slug: '' };
	try {
		sweep = await getSweepPosition({ fetch });
	} catch {
		// Leave the default "no resume point".
	}

	return {
		runs: data.runs,
		total: data.total,
		limit: data.limit,
		offset: data.offset,
		sweep
	};
}
