// poll(fn) runs fn on an interval that tracks the user's chosen refresh frequency, re-arming the
// timer whenever the frequency changes and cleaning up on unmount. It is the one helper every live
// page uses, so the "Refresh" dropdown in the top bar takes effect everywhere at once.
import { onMount } from 'svelte';
import { refreshMs } from '$lib/refresh.js';

export function poll(fn) {
	onMount(() => {
		let timer;
		const unsub = refreshMs.subscribe((ms) => {
			if (timer) clearInterval(timer);
			timer = setInterval(fn, ms);
		});
		return () => {
			unsub();
			if (timer) clearInterval(timer);
		};
	});
}
