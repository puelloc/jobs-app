import { error } from '@sveltejs/kit';
import { getJobLog, getRun, getTrace } from '$lib/api.js';

// Server-rendered: the first HTML response carries the run's status, any agent
// trace, and any job log written so far. The page then re-runs this load on an
// interval to pick up live steps while the run is in flight.
/** @type {import('./$types').PageLoad} */
export async function load({ params, fetch, depends }) {
	depends('data:run');

	let run;
	try {
		run = await getRun(params.id, { fetch });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load run');
	}

	let trace = { present: false, events: [] };
	let log = { present: false, log: '' };
	try {
		trace = await getTrace(params.id, { fetch });
		log = await getJobLog(params.id, { fetch });
	} catch {
		// A trace/log read that fails is not a page failure: the run still
		// renders, and the poll retries once the browser takes over.
	}

	return { run, trace, log };
}
