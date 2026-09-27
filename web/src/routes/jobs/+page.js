import { error } from '@sveltejs/kit';
import { getJobs } from '$lib/api.js';

// Server-rendered: the first HTML response already contains the first page of
// jobs. "Load more" fetches further pages client-side, so no offset is read here.
/** @type {import('./$types').PageLoad} */
export async function load({ fetch, depends }) {
	depends('data:jobs');

	let data;
	try {
		data = await getJobs({ limit: 25, offset: 0 }, { fetch });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load jobs');
	}

	return {
		jobs: data.jobs,
		limit: data.limit,
		offset: data.offset,
		total: data.total
	};
}
