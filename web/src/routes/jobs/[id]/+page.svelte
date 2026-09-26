<script>
	import {
		formatDate,
		formatEmploymentType,
		formatLocation,
		formatRelative,
		formatSalary,
		formatStatus
	} from '$lib/format.js';

	let { data } = $props();

	const job = $derived(data.job);
	// The source's company strings can carry a trailing space.
	const companyName = $derived(job.company_name?.trim() ?? '');

	const statusLabel = $derived(formatStatus(job.status));
	const statusClass = $derived(job.status === 'open' ? 'status-open' : 'status-closed');
	// "last seen" is when this app last observed the job, not why the status is
	// what it is: closure is decided by the latest scrape's seen set, never by
	// elapsed time (docs/scraper-design.md, "Freshness contract").
	const lastSeenRelative = $derived(formatRelative(job.last_seen_at));

	const employmentType = $derived(formatEmploymentType(job.employment_type));
	const location = $derived(formatLocation(job));
	const compensation = $derived(formatSalary(job.salary) || 'Not stated');
	const posted = $derived(formatDate(job.posted_at) || 'Not stated');
	const firstSeen = $derived(formatDate(job.first_seen_at));
	const lastSeen = $derived(formatDate(job.last_seen_at));
</script>

<svelte:head>
	<title>{job.title}</title>
</svelte:head>

<main>
	<p class="back"><a href="/">← Back to list</a></p>

	<h1>{job.title}</h1>
	<p class="company">{companyName}</p>

	<p class="status-line">
		<span class="status {statusClass}">{statusLabel}</span>
		<span class="seen">— last seen {lastSeenRelative}</span>
	</p>

	<dl class="facts">
		{#if employmentType}
			<div><dt>Employment type</dt><dd>{employmentType}</dd></div>
		{/if}
		{#if location}
			<div><dt>Location</dt><dd>{location}</dd></div>
		{/if}
		{#if job.is_us === true}
			<div><dt>Region</dt><dd>US</dd></div>
		{/if}
		<div><dt>Compensation</dt><dd>{compensation}</dd></div>
	</dl>

	<dl class="dates">
		<div><dt>Posted by the source</dt><dd>{posted}</dd></div>
		<div><dt>First seen by this app</dt><dd>{firstSeen}</dd></div>
		<div><dt>Last seen by this app</dt><dd>{lastSeen}</dd></div>
	</dl>

	<ul class="links">
		<li>
			<a href={job.listing_url} target="_blank" rel="noopener noreferrer">View listing</a>
		</li>
		{#if job.application_url}
			<li>
				<a href={job.application_url} target="_blank" rel="noopener noreferrer"
					>Apply on the company site</a
				>
			</li>
		{/if}
		{#if job.discovery_url}
			<li>
				<a href={job.discovery_url} target="_blank" rel="noopener noreferrer"
					>Original source page</a
				>
			</li>
		{/if}
	</ul>

	<section class="description">
		<h2>Description</h2>
		{#if job.description}
			<!-- Escaped as text on purpose: the payload may carry HTML, and v1
			     ships no sanitizer. It is never rendered as raw markup. -->
			<pre>{job.description}</pre>
		{:else}
			<p class="muted">No description provided.</p>
		{/if}
	</section>

	<p class="back"><a href="/">← Back to list</a></p>
</main>

<style>
	main {
		max-width: 1200px;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
	}

	.back {
		margin: 0 0 1rem;
		font-size: 0.9rem;
	}

	h1 {
		margin: 0;
		font-size: 1.6rem;
		line-height: 1.25;
	}

	.company {
		margin: 0.2rem 0 0;
		color: #4a4f55;
		font-size: 1.05rem;
	}

	.status-line {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin: 0.75rem 0 0;
	}

	.status {
		padding: 0.1rem 0.5rem;
		border-radius: 999px;
		border: 1px solid currentColor;
		font-size: 0.85rem;
		font-weight: 600;
	}

	.status-open {
		color: #1a7f37;
	}

	.status-closed {
		color: #6b7178;
	}

	.seen {
		color: #6b7178;
		font-size: 0.9rem;
	}

	.facts,
	.dates {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: 0.75rem 1.5rem;
		margin: 1.5rem 0 0;
		padding: 1rem 0;
		border-top: 1px solid #e6e8eb;
		border-bottom: 1px solid #e6e8eb;
	}

	.facts div,
	.dates div {
		margin: 0;
	}

	dt {
		margin: 0;
		color: #6b7178;
		font-size: 0.78rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	dd {
		margin: 0.15rem 0 0;
		font-size: 0.95rem;
	}

	.dates dd {
		color: #4a4f55;
		font-variant-numeric: tabular-nums;
	}

	.links {
		list-style: none;
		display: flex;
		flex-wrap: wrap;
		gap: 1.25rem;
		margin: 1.5rem 0 0;
		padding: 0;
	}

	.description {
		margin-top: 2rem;
	}

	.description h2 {
		margin: 0 0 0.5rem;
		font-size: 1rem;
	}

	pre {
		margin: 0;
		padding: 1rem;
		background: #f4f5f7;
		border: 1px solid #e6e8eb;
		border-radius: 4px;
		font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
		font-size: 0.85rem;
		line-height: 1.5;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.muted {
		color: #6b7178;
	}

	@media (max-width: 600px) {
		main {
			padding: 1.25rem 1rem 3rem;
		}
	}
</style>
