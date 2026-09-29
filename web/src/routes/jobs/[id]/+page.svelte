<script>
	import TraceView from '$lib/TraceView.svelte';
	import {
		NOT_STATED,
		formatJobStatus,
		formatLocation,
		formatSalary,
		formatUtc,
		humanize
	} from '$lib/format.js';

	let { data } = $props();

	const job = $derived(data.job);
	const trace = $derived(data.trace ?? { present: false, events: [] });
	const hasRun = $derived(Boolean(job.run_id));
</script>

<svelte:head>
	<title>{job.title} · Jobs dashboard</title>
	<meta name="description" content={`${job.title} at ${job.company_name}`} />
</svelte:head>

<main>
	<div class="heading">
		<h1>{job.title}</h1>
		<span class="badge badge-{job.status}">{formatJobStatus(job.status)}</span>
	</div>
	<p class="company">{job.company_name}</p>

	<section class="section">
		<h2>Facts</h2>
		<dl class="facts">
			<div>
				<dt>Employment type</dt>
				<dd>{job.employment_type ? humanize(job.employment_type) : NOT_STATED}</dd>
			</div>
			<div>
				<dt>Location</dt>
				<dd>{formatLocation(job) || NOT_STATED}</dd>
			</div>
			<div>
				<dt>Compensation</dt>
				<dd>{formatSalary(job.salary) || NOT_STATED}</dd>
			</div>
		</dl>
	</section>

	<section class="section">
		<h2>Dates</h2>
		<dl class="facts">
			<div>
				<dt>Posted by the source</dt>
				<dd>{job.posted_at ? formatUtc(job.posted_at) : NOT_STATED}</dd>
			</div>
			<div>
				<dt>First seen by this app</dt>
				<dd>{formatUtc(job.first_seen_at)}</dd>
			</div>
			<div>
				<dt>Last seen by this app</dt>
				<dd>{formatUtc(job.last_seen_at)}</dd>
			</div>
		</dl>
	</section>

	<section class="section">
		<h2>Links</h2>
		<ul class="links">
			<li>
				<a href={job.listing_url} target="_blank" rel="noreferrer">View listing</a>
			</li>
			{#if job.application_url}
				<li>
					<a href={job.application_url} target="_blank" rel="noreferrer">Apply on the company site</a>
				</li>
			{/if}
			{#if job.discovery_url}
				<li>
					<a href={job.discovery_url} target="_blank" rel="noreferrer">Original source page</a>
				</li>
			{/if}
		</ul>
	</section>

	<section class="section">
		<h2>Description</h2>
		<div class="description">
			{#if job.description}
				{job.description}
			{:else}
				{NOT_STATED}
			{/if}
		</div>
	</section>

	{#if hasRun}
		<section class="section">
			<h2>Why this job matched</h2>
			<p class="run-link">
				Scraped by <a href={`/runs/${job.run_id}`}>run #{job.run_id}</a> — the agent's
				reasoning for counting this role is below.
			</p>
			<TraceView events={trace.events} present={trace.present} />
		</section>
	{/if}

	<p><a href="/jobs" class="back">← Back to jobs</a></p>
</main>

<style>
	main {
		max-width: 960px;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
	}

	.heading {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	h1 {
		margin: 0;
		font-size: 1.5rem;
	}

	.company {
		margin: 0.4rem 0 1.5rem;
		color: #4a4f55;
		font-weight: 600;
	}

	.badge {
		padding: 0.05rem 0.5rem;
		border-radius: 999px;
		border: 1px solid currentColor;
		font-size: 0.75rem;
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

	.section h2 {
		margin: 0 0 0.5rem;
		font-size: 0.82rem;
		font-weight: 650;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: #6b7178;
	}

	.section + .section {
		margin-top: 1.75rem;
	}

	.facts {
		margin: 0;
		padding: 0.75rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.facts div {
		display: flex;
		gap: 1rem;
		padding: 0.35rem 0;
	}

	.facts div + div {
		border-top: 1px solid #eef0f3;
	}

	.facts dt {
		flex: 0 0 10rem;
		color: #6b7178;
	}

	.facts dd {
		margin: 0;
	}

	.links {
		list-style: none;
		margin: 0;
		padding: 0.75rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.links li + li {
		margin-top: 0.35rem;
	}

	.description {
		padding: 0.85rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
		white-space: pre-wrap;
		word-break: break-word;
		/* Keep a long posting from dominating the page: cap the height and scroll within the box. */
		max-height: 24rem;
		overflow-y: auto;
	}

	.run-link {
		margin: 0 0 0.5rem;
		color: #6b7178;
		font-size: 0.85rem;
	}

	.back {
		display: inline-block;
		margin-top: 1.5rem;
	}

	@media (max-width: 600px) {
		main {
			padding: 1.25rem 1rem 3rem;
		}

		.heading {
			flex-direction: column;
			align-items: flex-start;
			gap: 0.4rem;
		}

		.facts div {
			flex-direction: column;
			gap: 0.1rem;
		}

		.facts dt {
			flex: none;
		}
	}
</style>
