import { error } from '@sveltejs/kit';
import { getJobs } from '$lib/api.js';

// The status filter mirrors the API's own default: live listings unless the URL asks
// for another status. A value the API would reject falls back to the default, so a
// hand-edited ?status=bogus renders listings rather than an error page.
const DEFAULT_STATUS = 'open';
const STATUSES = ['open', 'closed', 'filled', 'unknown', 'all'];

// Server-rendered: the first HTML response already contains the first page of
// jobs. "Load more" fetches further pages client-side, so no offset is read here.
/** @type {import('./$types').PageLoad} */
export async function load({ url, fetch, depends }) {
	depends('data:jobs');

	const raw = (url.searchParams.get('status') ?? '').toLowerCase();
	const status = STATUSES.includes(raw) ? raw : DEFAULT_STATUS;

	let data;
	try {
		data = await getJobs({ limit: 25, offset: 0, status }, { fetch });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load jobs');
	}

	return {
		jobs: data.jobs,
		limit: data.limit,
		offset: data.offset,
		total: data.total,
		status
	};
}
