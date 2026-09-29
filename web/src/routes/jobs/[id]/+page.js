import { error } from '@sveltejs/kit';
import { getJob, getTrace } from '$lib/api.js';

// Server-rendered: the first HTML response already contains the job, including
// its description and — when the job is linked to a scrape run — the agent trace
// that explains why it was scraped.
/** @type {import('./$types').PageLoad} */
export async function load({ params, fetch, depends }) {
	depends('data:job');

	let data;
	try {
		data = await getJob(params.id, { fetch });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load job');
	}

	let trace = { present: false, events: [] };
	if (data.run_id) {
		try {
			trace = await getTrace(data.run_id, { fetch });
		} catch {
			// A missing trace is not a page failure: the job still renders.
		}
	}

	return { job: data, trace };
}
