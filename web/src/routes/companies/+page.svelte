<script>
	import { getCompanies } from '$lib/api.js';
	import { NOT_STATED, formatAttemptCount, formatVerdict } from '$lib/format.js';

	let { data } = $props();

	// load() owns the first page; "load more" appends further pages client-side.
	// The initial values render during SSR, and the effect re-syncs them whenever
	// load() runs again after a filter change.
	let companies = $state(data.companies ?? []);
	let offset = $state(data.offset + (data.companies?.length ?? 0));
	let loadingMore = $state(false);
	let loadError = $state('');

	$effect(() => {
		companies = data.companies ?? [];
		offset = data.offset + (data.companies?.length ?? 0);
	});

	const total = $derived(data.total ?? 0);
	const hasMore = $derived(offset < total);

	async function loadMore() {
		loadingMore = true;
		loadError = '';
		try {
			const page = await getCompanies({
				limit: data.limit,
				offset,
				index: data.index,
				resolution: data.resolution,
				search: data.search
			});
			companies = [...companies, ...(page.companies ?? [])];
			offset += page.companies?.length ?? 0;
		} catch (failure) {
			loadError = failure?.message ?? 'Could not load more companies';
		} finally {
			loadingMore = false;
		}
	}
</script>

<svelte:head>
	<title>Companies · Jobs dashboard</title>
	<meta name="description" content="Career-site directory and validation verdicts" />
</svelte:head>

<main>
	<div class="heading">
		<h1>Companies</h1>
		<p class="sub">{total} companies</p>
	</div>

	<form class="filters" method="get" action="/companies">
		<label>
			<span>Resolution</span>
			<select name="resolution">
				<option value="" selected={data.resolution === ''}>All</option>
				<option value="resolved" selected={data.resolution === 'resolved'}>Resolved</option>
				<option value="unresolved" selected={data.resolution === 'unresolved'}>Unresolved</option>
				<option value="unattempted" selected={data.resolution === 'unattempted'}>Unattempted</option>
			</select>
		</label>
		<label>
			<span>Index</span>
			<select name="index">
				<option value="" selected={data.index === ''}>All indexes</option>
				<option value="sp500" selected={data.index === 'sp500'}>S&amp;P 500</option>
				<option value="sp400" selected={data.index === 'sp400'}>S&amp;P 400</option>
				<option value="sp600" selected={data.index === 'sp600'}>S&amp;P 600</option>
			</select>
		</label>
		<label class="search">
			<span>Search</span>
			<input type="search" name="search" placeholder="Name or slug" value={data.search} />
		</label>
		<button type="submit">Filter</button>
		{#if data.resolution || data.index || data.search}
			<a class="clear" href="/companies">Clear</a>
		{/if}
	</form>

	{#if companies.length === 0}
		<p class="empty">No companies match. Adjust the filters and try again.</p>
	{:else}
		<ul class="company-list">
			{#each companies as company (company.id)}
				<li class="company">
					<div class="company-main">
						<a href={`/companies/${company.id}`} class="name">{company.name}</a>
						<span class="slug">{company.slug}</span>
						<span class="badge badge-{company.career_site_url_verdict ?? 'none'}">
							{formatVerdict(company.career_site_url_verdict)}
						</span>
					</div>
					<div class="meta">
						{#if company.career_site_url}
							<a href={company.career_site_url} target="_blank" rel="noreferrer">
								{company.career_site_url}
							</a>
						{:else}
							<span class="quiet">{NOT_STATED}</span>
						{/if}
						<span>{formatAttemptCount(company.attempt_count)}</span>
					</div>
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

	.filters select,
	.filters input {
		font: inherit;
		padding: 0.35rem 0.5rem;
		border: 1px solid #d4d9e0;
		border-radius: 6px;
		background: #ffffff;
		color: #171b21;
	}

	.filters .search input {
		width: 14rem;
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

	.filters .clear {
		align-self: center;
		font-size: 0.85rem;
	}

	.company-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.company {
		padding: 0.7rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.company-main {
		display: flex;
		align-items: center;
		gap: 0.6rem;
	}

	.name {
		font-weight: 600;
	}

	.slug {
		color: #8a9099;
		font-size: 0.82rem;
	}

	.badge {
		padding: 0.05rem 0.5rem;
		border-radius: 999px;
		border: 1px solid currentColor;
		font-size: 0.72rem;
		font-weight: 650;
		letter-spacing: 0.02em;
	}

	.badge-confirmed {
		color: #1a7f37;
	}

	.badge-wrong {
		color: #c62828;
	}

	.badge-unverifiable {
		color: #9a6b00;
	}

	.badge-none {
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
		word-break: break-all;
	}

	.quiet {
		color: #8a9099;
	}

	.empty {
		margin: 0.25rem 0 0;
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
