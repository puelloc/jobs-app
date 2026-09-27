<script>
	/**
	 * The churn view: which companies' accepted careers URL changed between two
	 * runs. The directory shows the value that is stored now; this shows the
	 * value moving, which is the only way a bad re-resolution is visible without
	 * opening every company's trail by hand.
	 */
	let { data } = $props();

	const indexes = [
		{ value: '', label: 'All indices' },
		{ value: 'sp500', label: 'S&P 500' },
		{ value: 'sp400', label: 'S&P 400' },
		{ value: 'sp600', label: 'S&P 600' }
	];

	// Links, not component state, so each filtered view is shareable.
	function href(next) {
		const params = new URLSearchParams();
		const merged = { index: data.index, search: data.search, ...next };
		for (const [key, value] of Object.entries(merged)) {
			if (value) params.set(key, value);
		}
		const qs = params.toString();
		return qs ? `/companies/churn?${qs}` : '/companies/churn';
	}

	// Derived, not one-time initializers: the filter links navigate within this
	// route, so `data` is replaced while the component instance is retained.
	const pageSize = $derived(data.limit);
	const hasPrev = $derived(data.offset > 0);
	const hasNext = $derived(data.offset + data.changes.length < data.total);
</script>

<svelte:head><title>Careers URL changes</title></svelte:head>

<h1>Careers URL changes</h1>
<p class="count">
	{data.total}
	{data.total === 1 ? 'change' : 'changes'}
	{#if data.index || data.search}(filtered){/if}
	· <a href="/companies">all companies →</a>
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
	{#if data.search}
		<span class="group"><a href={href({ search: '' })}>Clear “{data.search}”</a></span>
	{/if}
</nav>

<form class="search" method="GET" action="/companies/churn">
	{#if data.index}<input type="hidden" name="index" value={data.index} />{/if}
	<input
		type="search"
		name="search"
		value={data.search}
		placeholder="Company name or slug"
		aria-label="Search companies"
	/>
	<button type="submit">Search</button>
</form>

{#if data.changes.length === 0}
	<p class="empty">No careers URL has changed between runs.</p>
{:else}
	<table>
		<thead>
			<tr>
				<th>Company</th>
				<th>Previous URL</th>
				<th>Current URL</th>
			</tr>
		</thead>
		<tbody>
			{#each data.changes as change (`${change.company_id}:${change.from.run_id}:${change.to.run_id}`)}
				<tr>
					<td>
						<a href={`/companies/${change.company_id}`}>{change.name}</a>
						<div class="slug">{change.slug}</div>
					</td>
					<td>
						<a href={change.from.url} rel="noopener noreferrer" target="_blank">
							{change.from.url}
						</a>
						<div class="slug">
							run {change.from.run_id}{change.from.title ? ` · ${change.from.title}` : ''}
						</div>
					</td>
					<td>
						<a href={change.to.url} rel="noopener noreferrer" target="_blank">
							{change.to.url}
						</a>
						<div class="slug">
							run {change.to.run_id}{change.to.title ? ` · ${change.to.title}` : ''}
						</div>
					</td>
				</tr>
			{/each}
		</tbody>
	</table>

	<nav class="paging">
		{#if hasPrev}
			<a href={href({ offset: String(Math.max(0, data.offset - pageSize)) })}>← Previous</a>
		{/if}
		<span>{data.offset + 1}–{data.offset + data.changes.length} of {data.total}</span>
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
		margin-bottom: 0.75rem;
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
	.search {
		display: flex;
		gap: 0.5rem;
		margin-bottom: 1rem;
	}
	.search input {
		padding: 0.3rem 0.5rem;
		border: 1px solid #c8ccd2;
		border-radius: 4px;
		font: inherit;
	}
	.search button {
		padding: 0.3rem 0.8rem;
		border: 1px solid #1552c4;
		border-radius: 4px;
		background: #1552c4;
		color: #fff;
		font: inherit;
		cursor: pointer;
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
		overflow-wrap: anywhere;
	}
	.slug {
		color: #7a8089;
		font-size: 0.78rem;
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
