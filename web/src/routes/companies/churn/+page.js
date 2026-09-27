import { error } from '@sveltejs/kit';
import { getChurn } from '$lib/api.js';

// The churn page is the history view of the directory: same server-rendered
// pattern, with the filters in the URL so a filtered view is shareable.
/** @type {import('./$types').PageLoad} */
export async function load({ url }) {
	const index = url.searchParams.get('index') ?? '';
	const search = url.searchParams.get('search') ?? '';
	const offset = Number(url.searchParams.get('offset') ?? '0') || 0;
	const limit = 50;

	let data;
	try {
		data = await getChurn({ index, search, limit, offset }, { origin: url.origin });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load careers URL changes');
	}

	return {
		changes: data.changes,
		total: data.total,
		limit: data.limit,
		offset: data.offset,
		index,
		search
	};
}
