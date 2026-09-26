import { error } from '@sveltejs/kit';
import { getJob } from '$lib/api.js';

// Server-rendered, like the list: the job is in the first HTML response. A 404
// from the API (a genuinely unknown id - rows are never deleted) becomes a 404
// error page.
/** @type {import('./$types').PageLoad} */
export async function load({ params, url }) {
	let job;
	try {
		job = await getJob(params.id, { origin: url.origin });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load this job');
	}

	return { job };
}
