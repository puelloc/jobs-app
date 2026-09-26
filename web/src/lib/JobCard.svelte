<script>
	import {
		formatDate,
		formatEmploymentType,
		formatLocation,
		formatSalary,
		formatStatus
	} from '$lib/format.js';

	let { job } = $props();

	// Everything below is derived from the job prop, which is replaced when the
	// list re-renders, so these are $derived rather than one-time initializers.
	const salary = $derived(formatSalary(job.salary));
	const statusLabel = $derived(formatStatus(job.status));
	const statusClass = $derived(job.status === 'open' ? 'status-open' : 'status-closed');
	const location = $derived(formatLocation(job));
	const employmentType = $derived(formatEmploymentType(job.employment_type));
	const posted = $derived(formatDate(job.posted_at));
	const firstSeen = $derived(formatDate(job.first_seen_at));
	const lastSeen = $derived(formatDate(job.last_seen_at));

	// company_name is never blank: the API resolves it and sends
	// "Unknown company" when the join finds no row. Segments are trimmed
	// (the source's company strings can carry a trailing space) and null
	// segments are dropped.
	const subtitle = $derived(
		[job.company_name, employmentType, location]
			.map((part) => part?.trim())
			.filter(Boolean)
			.join(' · ')
	);
</script>

<article class="card">
	<h2><a href={`/jobs/${job.id}`}>{job.title}</a></h2>
	<p class="subtitle">{subtitle}</p>
	<p class="meta">
		<span class="status {statusClass}">{statusLabel}</span>
		{#if salary}<span class="salary">{salary}</span>{/if}
		{#if posted}<span class="date">Posted {posted}</span>{/if}
		<span class="date">First seen {firstSeen}</span>
		<span class="date">Last seen {lastSeen}</span>
	</p>
</article>

<style>
	.card {
		padding: 0.9rem 0;
		border-bottom: 1px solid #e6e8eb;
	}

	h2 {
		margin: 0;
		font-size: 1.05rem;
		font-weight: 600;
		line-height: 1.3;
	}

	h2 a {
		text-decoration-line: none;
	}

	h2 a:hover {
		text-decoration-line: underline;
	}

	.subtitle {
		margin: 0.15rem 0 0;
		color: #4a4f55;
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem 0.75rem;
		margin: 0.4rem 0 0;
		font-size: 0.82rem;
		color: #6b7178;
	}

	.status {
		padding: 0.05rem 0.45rem;
		border-radius: 999px;
		border: 1px solid currentColor;
		font-weight: 600;
		letter-spacing: 0.02em;
	}

	.status-open {
		color: #1a7f37;
	}

	.status-closed {
		color: #6b7178;
	}

	.salary {
		color: #4a4f55;
		font-variant-numeric: tabular-nums;
	}

	.date {
		font-variant-numeric: tabular-nums;
	}
</style>
