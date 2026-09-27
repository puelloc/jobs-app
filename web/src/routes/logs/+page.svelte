<script>
	import { invalidate } from '$app/navigation';
	import { poll } from '$lib/poll.js';

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
		<p class="sub">Tail of the Go API server's stderr · refreshes every 3s</p>
	</div>
	{#if !log}
		<p class="quiet">No log output yet.</p>
	{:else}
		<pre class="log">{log}</pre>
	{/if}
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

	.quiet {
		margin: 0;
		color: #6b7178;
	}

	.log {
		margin: 0;
		padding: 0.9rem 1rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
		white-space: pre-wrap;
		word-break: break-word;
		font-size: 0.8rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
		max-height: 70vh;
		overflow: auto;
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
