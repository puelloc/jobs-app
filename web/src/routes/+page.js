import { error } from '@sveltejs/kit';
import { getJobs } from '$lib/api.js';

// Server-rendered: this runs during SSR so the first HTML response already
// contains the jobs. Nothing fetches in the browser on first load.
/** @type {import('./$types').PageLoad} */
export async function load({ url }) {
	let data;
	try {
		data = await getJobs({ limit: 25, offset: 0 }, { origin: url.origin });
	} catch (failure) {
		// The API's own message when it sent an error envelope; a readable
		// fallback when the failure never reached the API at all.
		error(failure?.status ?? 500, failure?.message ?? 'Could not load jobs');
	}

	return {
		jobs: data.jobs,
		total: data.total,
		limit: data.limit,
		offset: data.offset
	};
}
