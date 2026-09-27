import { error } from '@sveltejs/kit';
import { getJob } from '$lib/api.js';

// Server-rendered: the first HTML response already contains the job, including
// its description.
/** @type {import('./$types').PageLoad} */
export async function load({ params, url, depends }) {
	depends('data:job');

	let data;
	try {
		data = await getJob(params.id, { origin: url.origin });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load job');
	}

	return { job: data };
}
