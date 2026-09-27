<script>
	/**
	 * The company directory: what the index bootstrap and the careers resolver
	 * between them produced, with the three states a single NULL check would
	 * conflate made visible.
	 */
	let { data } = $props();

	const indexes = [
		{ value: '', label: 'All indices' },
		{ value: 'sp500', label: 'S&P 500' },
		{ value: 'sp400', label: 'S&P 400' },
		{ value: 'sp600', label: 'S&P 600' }
	];

	const resolutions = [
		{ value: '', label: 'Any state' },
		{ value: 'resolved', label: 'Resolved' },
		{ value: 'unresolved', label: 'Looked, not found' },
		{ value: 'unattempted', label: 'Never looked at' }
	];

	// The filters are ordinary links, so each view is shareable and the back
	// button works. Rebuilding the query from scratch drops empty values rather
	// than sending them, matching what the API treats as "no filter".
	function href(next) {
		const params = new URLSearchParams();
		const merged = {
			index: data.index,
			resolution: data.resolution,
			search: data.search,
			...next
		};
		for (const [key, value] of Object.entries(merged)) {
			if (value) params.set(key, value);
		}
		const qs = params.toString();
		return qs ? `/companies?${qs}` : '/companies';
	}

	// Derived, not one-time initializers: the filter links are client-side
	// navigations within this same route, so `data` is replaced while the
	// component instance is retained. A plain `const` would freeze the paging
	// state from the first visit (Svelte warns state_referenced_locally).
	const pageSize = $derived(data.limit);
	const hasPrev = $derived(data.offset > 0);
	const hasNext = $derived(data.offset + data.companies.length < data.total);
</script>

<svelte:head><title>Companies</title></svelte:head>

<h1>Companies</h1>
<p class="count">
	{data.total} {data.total === 1 ? 'company' : 'companies'}
	{#if data.index || data.resolution || data.search}(filtered){/if}
	· <a href="/">browse jobs →</a>
	· <a href="/companies/churn">URL changes →</a>
</p>

<nav class="filters">
	<span class="group">
		{#each indexes as option}
			<a
				href={href({ index: option.value, offset: '' })}
				class:active={data.index === option.value}>{option.label}</a
			>
		{/each}
	</span>
	<span class="group">
		{#each resolutions as option}
			<a
				href={href({ resolution: option.value, offset: '' })}
				class:active={data.resolution === option.value}>{option.label}</a
			>
		{/each}
	</span>
	{#if data.search}
		<span class="group"><a href={href({ search: '' })}>Clear “{data.search}”</a></span>
	{/if}
</nav>

{#if data.companies.length === 0}
	<p class="empty">No companies match this filter.</p>
{:else}
	<table>
		<thead>
			<tr>
				<th>Name</th>
				<th>Index</th>
				<th>Industry</th>
				<th>Website</th>
				<th>Careers site</th>
			</tr>
		</thead>
		<tbody>
			{#each data.companies as company (company.id)}
				<tr>
					<td>
						<a href={`/companies/${company.id}`}>{company.name}</a>
						<div class="slug">{company.slug}</div>
					</td>
					<td>{company.index_membership ?? '—'}</td>
					<td>{company.industry ?? '—'}</td>
					<td>
						{#if company.website}
							<a href={company.website} rel="noopener noreferrer" target="_blank">
								{company.website.replace(/^https?:\/\//, '')}
							</a>
							{#if company.website_source}
								<div class="slug">via {company.website_source}</div>
							{/if}
						{:else}
							<span class="missing">not resolved</span>
						{/if}
					</td>
					<td>
						{#if company.career_site_url}
							<a href={company.career_site_url} rel="noopener noreferrer" target="_blank">
								{company.career_site_url.replace(/^https?:\/\//, '')}
							</a>
							{#if company.career_site_source}
								<div class="slug">via {company.career_site_source}</div>
							{/if}
						{:else if company.attempt_count > 0}
							<span class="missing">looked, none found ({company.attempt_count})</span>
						{:else}
							<span class="missing">never looked at</span>
						{/if}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>

	<nav class="paging">
		{#if hasPrev}
			<a href={href({ offset: String(Math.max(0, data.offset - pageSize)) })}>← Previous</a>
		{/if}
		<span>{data.offset + 1}–{data.offset + data.companies.length} of {data.total}</span>
		{#if hasNext}
			<a href={href({ offset: String(data.offset + pageSize) })}>Next →</a>
		{/if}
	</nav>
{/if}

<style>
	h1 {
		font-size: 1.5rem;
		margin: 1.5rem 0 0.25rem;
	}
	.count {
		color: #5b6169;
		margin: 0 0 1rem;
	}
	.filters {
		display: flex;
		flex-wrap: wrap;
		gap: 1rem;
		margin-bottom: 1rem;
	}
	.group {
		display: flex;
		gap: 0.5rem;
	}
	.group a {
		text-decoration: none;
		padding: 0.15rem 0.5rem;
		border-radius: 4px;
	}
	.group a.active {
		background: #1552c4;
		color: #fff;
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.9rem;
	}
	th {
		text-align: left;
		border-bottom: 2px solid #d9dce1;
		padding: 0.4rem 0.6rem;
	}
	td {
		border-bottom: 1px solid #eceef1;
		padding: 0.5rem 0.6rem;
		vertical-align: top;
	}
	.slug {
		color: #7a8089;
		font-size: 0.78rem;
	}
	.missing {
		color: #a3600a;
	}
	.empty {
		color: #5b6169;
	}
	.paging {
		display: flex;
		gap: 1rem;
		align-items: center;
		margin: 1rem 0 2rem;
	}
</style>
