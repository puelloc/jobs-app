import { error } from '@sveltejs/kit';
import { getPipelineStatus, getRuns, getSweepPosition } from '$lib/api.js';

// Server-rendered: this runs during SSR so the first HTML response already
// contains the runs. The page then re-runs it on an interval via invalidate to
// pick up live status.
/** @type {import('./$types').PageLoad} */
export async function load({ url, fetch, depends }) {
	depends('data:runs');

	// q filters the list by platform/status (the search box on the page).
	const q = url.searchParams.get('q') ?? '';

	let data;
	try {
		data = await getRuns({ limit: 100, offset: 0, q }, { fetch });
	} catch (failure) {
		// The API's own message when it sent an error envelope; a readable
		// fallback when the failure never reached the API at all.
		error(failure?.status ?? 500, failure?.message ?? 'Could not load runs');
	}

	// The resume point and the readiness counts are best-effort: a failure to read either must not
	// break the page, which still has the runs to show.
	let sweep = { present: false, slug: '' };
	try {
		sweep = await getSweepPosition({ fetch });
	} catch {
		// Leave the default "no resume point".
	}

	let status = null;
	try {
		status = await getPipelineStatus({ fetch });
	} catch {
		// Leave status null; the pipeline panel then says it cannot tell.
	}

	return {
		runs: data.runs,
		total: data.total,
		limit: data.limit,
		offset: data.offset,
		q,
		sweep,
		status
	};
}
