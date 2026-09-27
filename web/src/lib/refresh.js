// The user-chosen poll interval for every live view. Persisted to localStorage so it survives
// reloads and redeploys. The top-bar "Refresh" dropdown writes this store.
import { writable } from 'svelte/store';
import { browser } from '$app/environment';

const DEFAULT_MS = 3000;
const KEY = 'jobs.refreshMs';

function initial() {
	if (browser) {
		try {
			const saved = Number(localStorage.getItem(KEY));
			if (Number.isFinite(saved) && saved > 0) return saved;
		} catch {
			// fall through to the default
		}
	}
	return DEFAULT_MS;
}

export const refreshMs = writable(initial());

refreshMs.subscribe((ms) => {
	if (browser) {
		try {
			localStorage.setItem(KEY, String(ms));
		} catch {
			// storage may be unavailable; the in-memory store still works
		}
	}
});
