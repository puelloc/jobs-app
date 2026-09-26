<script>
	import { navigating } from '$app/state';
	import JobCard from '$lib/JobCard.svelte';

	let { data } = $props();

	// load() owns this data: it is replaced wholesale on navigation and never
	// mutated in place, so it needs no local state and no deep reactivity.
	const jobs = $derived(data.jobs);
	const total = $derived(data.total);

	// `navigating` is always an object; `to` is null unless a client-side
	// navigation is in flight. That keeps this line off the server-rendered
	// first paint, which already contains the jobs.
	const isLoading = $derived(navigating.to !== null);
</script>

<svelte:head>
	<title>Jobs</title>
	<meta name="description" content="Scraped software engineering job listings" />
</svelte:head>

<main>
	<header>
		<h1>Jobs</h1>
		<p class="count">
			{total} {total === 1 ? 'job' : 'jobs'} · <a href="/companies">browse companies →</a>
		</p>
	</header>

	{#if isLoading}
		<p class="loading">Loading jobs…</p>
	{/if}

	{#if total === 0}
		<p class="empty">No jobs yet. Run the scraper, then reload this page.</p>
	{:else}
		<ul class="jobs">
			{#each jobs as job (job.id)}
				<li>
					<JobCard {job} />
				</li>
			{/each}
		</ul>
	{/if}
</main>

<style>
	main {
		max-width: 1200px;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
	}

	header {
		display: flex;
		align-items: baseline;
		gap: 0.75rem;
		border-bottom: 2px solid #1c1e21;
		padding-bottom: 0.5rem;
	}

	h1 {
		margin: 0;
		font-size: 1.5rem;
	}

	.count {
		margin: 0;
		color: #6b7178;
		font-size: 0.9rem;
	}

	.loading,
	.empty {
		margin: 1.5rem 0 0;
		color: #4a4f55;
	}

	.jobs {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	@media (max-width: 600px) {
		main {
			padding: 1.25rem 1rem 3rem;
		}
	}
</style>
