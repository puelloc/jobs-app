import { getServerLog } from '$lib/api.js';

// Server-rendered with the current tail, then polled client-side.
/** @type {import('./$types').PageLoad} */
export async function load({ fetch, depends }) {
	depends('data:serverlog');

	let log = '';
	try {
		log = (await getServerLog({ fetch })).log ?? '';
	} catch {
		// A missing/unreadable log is not a page failure; the poll retries.
	}

	return { log };
}
