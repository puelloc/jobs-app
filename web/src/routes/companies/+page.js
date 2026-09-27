import { error } from '@sveltejs/kit';
import { getCompanies } from '$lib/api.js';

// Server-rendered: the first HTML response already contains the first page of
// the directory. Filters come from the URL (the filter bar is a plain GET form),
// so a filter change is a normal navigation that re-runs this load.
/** @type {import('./$types').PageLoad} */
export async function load({ url, depends }) {
	depends('data:companies');

	const index = url.searchParams.get('index') ?? '';
	const resolution = url.searchParams.get('resolution') ?? '';
	const search = url.searchParams.get('search') ?? '';

	let data;
	try {
		data = await getCompanies(
			{ limit: 25, offset: 0, index, resolution, search },
			{ origin: url.origin }
		);
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load companies');
	}

	return {
		companies: data.companies,
		limit: data.limit,
		offset: data.offset,
		total: data.total,
		index,
		resolution,
		search
	};
}
