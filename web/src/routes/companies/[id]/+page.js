import { error } from '@sveltejs/kit';
import { getCompany } from '$lib/api.js';

// Server-rendered: the first HTML response already contains the company and its
// whole resolution trail.
/** @type {import('./$types').PageLoad} */
export async function load({ params, url, depends }) {
	depends('data:company');

	let data;
	try {
		data = await getCompany(params.id, { origin: url.origin });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load company');
	}

	return { company: data.company, attempts: data.attempts };
}
