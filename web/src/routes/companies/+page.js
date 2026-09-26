import { error } from '@sveltejs/kit';
import { getCompanies } from '$lib/api.js';

// Server-rendered, same as the jobs list: the filters live in the URL so a
// filtered view is a linkable address rather than component state.
/** @type {import('./$types').PageLoad} */
export async function load({ url }) {
	const index = url.searchParams.get('index') ?? '';
	const resolution = url.searchParams.get('resolution') ?? '';
	const search = url.searchParams.get('search') ?? '';
	const offset = Number(url.searchParams.get('offset') ?? '0') || 0;
	const limit = 50;

	let data;
	try {
		data = await getCompanies({ index, resolution, search, limit, offset }, { origin: url.origin });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load companies');
	}

	return {
		companies: data.companies,
		total: data.total,
		limit: data.limit,
		offset: data.offset,
		index,
		resolution,
		search
	};
}
