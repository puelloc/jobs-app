import { error } from '@sveltejs/kit';
import { getRun, getTrace } from '$lib/api.js';

// Server-rendered: the first HTML response carries the run's status and any
// trace written so far. The page then re-runs this load on an interval to pick
// up live agent steps while the run is in flight.
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
	try {
		trace = await getTrace(params.id, { fetch });
	} catch {
		// A trace read that fails is not a page failure: the run still renders,
		// and the poll retries once the browser takes over.
	}

	return { run, trace };
}
