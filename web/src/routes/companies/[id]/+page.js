import { error } from '@sveltejs/kit';
import { getCompany } from '$lib/api.js';

/** @type {import('./$types').PageLoad} */
export async function load({ params, url }) {
	let data;
	try {
		data = await getCompany(params.id, { origin: url.origin });
	} catch (failure) {
		error(failure?.status ?? 500, failure?.message ?? 'Could not load the company');
	}
	return { company: data.company, attempts: data.attempts };
}
