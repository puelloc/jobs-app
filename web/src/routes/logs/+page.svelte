<script>
	import { invalidate } from '$app/navigation';
	import { poll } from '$lib/poll.js';
	import LogView from '$lib/LogView.svelte';

	let { data } = $props();

	const log = $derived(data.log ?? '');

	// Poll so the tail follows a running job without a manual refresh.
	poll(() => invalidate('data:serverlog'));
</script>

<svelte:head>
	<title>Server log · Jobs dashboard</title>
	<meta name="description" content="The Go API server's runtime log" />
</svelte:head>

<main>
	<div class="heading">
		<h1>Server log</h1>
		<p class="sub">Most recent first · paginated · refreshes live</p>
	</div>
	<LogView text={log} />
</main>

<style>
	main {
		max-width: 960px;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
	}

	.heading {
		display: flex;
		align-items: baseline;
		gap: 0.75rem;
		margin-bottom: 1.25rem;
	}

	h1 {
		margin: 0;
		font-size: 1.5rem;
	}

	.sub {
		margin: 0;
		color: #6b7178;
		font-size: 0.9rem;
	}

	@media (max-width: 600px) {
		main {
			padding: 1.25rem 1rem 3rem;
		}

		.heading {
			flex-direction: column;
			gap: 0.1rem;
		}
	}
</style>

