<script>
	import { getJobs } from '$lib/api.js';
	import { formatJobStatus, formatSalary, formatUtc, humanize } from '$lib/format.js';

	let { data } = $props();

	// load() owns the first page; "load more" appends further pages client-side.
	// No polling, so the initial values never go stale in place.
	let jobs = $state(data.jobs ?? []);
	let offset = $state(data.offset + (data.jobs?.length ?? 0));
	let loadingMore = $state(false);
	let loadError = $state('');

	const total = $derived(data.total ?? 0);
	const hasMore = $derived(offset < total);
	// "3 open jobs" / "10 jobs": the count is always scoped by the active filter.
	const scopeLabel = $derived(data.status === 'all' ? 'jobs' : `${data.status} jobs`);

	async function loadMore() {
		loadingMore = true;
		loadError = '';
		try {
			// The status filter carries into paging, or page two would mix statuses.
			const page = await getJobs({ limit: data.limit, offset, status: data.status });
			jobs = [...jobs, ...(page.jobs ?? [])];
			offset += page.jobs?.length ?? 0;
		} catch (failure) {
			loadError = failure?.message ?? 'Could not load more jobs';
		} finally {
			loadingMore = false;
		}
	}
</script>

<svelte:head>
	<title>Jobs · Jobs dashboard</title>
	<meta name="description" content="Scraped job listings" />
</svelte:head>

<main>
	<div class="heading">
		<h1>Jobs</h1>
		<p class="sub">{total} {scopeLabel}</p>
	</div>

	<form class="filters" method="get" action="/jobs">
		<label>
			<span>Status</span>
			<select name="status">
				<option value="open" selected={data.status === 'open'}>Open</option>
				<option value="closed" selected={data.status === 'closed'}>Closed</option>
				<option value="filled" selected={data.status === 'filled'}>Filled</option>
				<option value="unknown" selected={data.status === 'unknown'}>Unknown</option>
				<option value="all" selected={data.status === 'all'}>All statuses</option>
			</select>
		</label>
		<button type="submit">Filter</button>
	</form>

	{#if jobs.length === 0}
		<p class="empty">No {scopeLabel} to show. Try another status, or run a scrape.</p>
	{:else}
		<ul class="job-list">
			{#each jobs as job (job.id)}
				<li class="job">
					<div class="job-main">
						<a href={`/jobs/${job.id}`} class="title">{job.title}</a>
						<span class="badge badge-{job.status}">{formatJobStatus(job.status)}</span>
					</div>
					<div class="meta">
						<span class="company">{job.company_name}</span>
						{#if job.employment_type}
							<span>{humanize(job.employment_type)}</span>
						{/if}
						{#if job.location_text || job.country}
							<span>{job.location_text ?? job.country}</span>
						{/if}
						{#if job.is_remote}
							<span class="remote">Remote</span>
						{/if}
						{#if formatSalary(job.salary)}
							<span>{formatSalary(job.salary)}</span>
						{/if}
					</div>
					{#if job.posted_at}
						<div class="posted">Posted {formatUtc(job.posted_at)}</div>
					{/if}
				</li>
			{/each}
		</ul>

		<div class="loadmore">
			{#if hasMore}
				<button onclick={loadMore} disabled={loadingMore}>
					{loadingMore ? 'Loading…' : 'Load more'}
				</button>
			{/if}
			{#if loadError}
				<p class="error">{loadError}</p>
			{/if}
		</div>
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

	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: 0.75rem;
		margin-bottom: 1.25rem;
		padding: 0.85rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.filters label {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
	}

	.filters label span {
		font-size: 0.72rem;
		font-weight: 650;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: #6b7178;
	}

	.filters select {
		font: inherit;
		padding: 0.35rem 0.5rem;
		border: 1px solid #d4d9e0;
		border-radius: 6px;
		background: #ffffff;
		color: #171b21;
	}

	.filters button {
		font: inherit;
		padding: 0.35rem 0.9rem;
		border: 1px solid #1a56c4;
		border-radius: 6px;
		background: #1a56c4;
		color: #ffffff;
		cursor: pointer;
	}

	.job-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.job {
		padding: 0.7rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.job-main {
		display: flex;
		align-items: center;
		gap: 0.6rem;
	}

	.title {
		font-weight: 600;
	}

	.badge {
		padding: 0.05rem 0.5rem;
		border-radius: 999px;
		border: 1px solid currentColor;
		font-size: 0.72rem;
		font-weight: 650;
		letter-spacing: 0.02em;
	}

	.badge-open {
		color: #1a7f37;
	}

	.badge-closed {
		color: #8a9099;
	}

	.badge-filled,
	.badge-unknown {
		color: #8a9099;
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.25rem 0.9rem;
		margin-top: 0.3rem;
		font-size: 0.82rem;
		color: #6b7178;
	}

	.company {
		font-weight: 600;
		color: #4a4f55;
	}

	.remote {
		color: #1a56c4;
	}

	.posted {
		margin-top: 0.3rem;
		font-size: 0.78rem;
		color: #8a9099;
		font-variant-numeric: tabular-nums;
	}

	.empty {
		margin: 0;
		color: #6b7178;
	}

	.loadmore {
		margin-top: 1rem;
	}

	.loadmore button {
		font: inherit;
		padding: 0.4rem 1rem;
		border: 1px solid #d4d9e0;
		border-radius: 6px;
		background: #ffffff;
		color: #171b21;
		cursor: pointer;
	}

	.loadmore button:disabled {
		opacity: 0.6;
		cursor: default;
	}

	.error {
		margin: 0.5rem 0 0;
		font-size: 0.85rem;
		color: #c62828;
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
