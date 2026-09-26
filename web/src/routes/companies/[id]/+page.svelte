<script>
	/**
	 * One company: the resolved values, and the whole trail that produced them.
	 *
	 * The trail is the point of this page. A wrong careers URL is only
	 * diagnosable next to the candidates that were refused before it, so
	 * rejected attempts are shown with equal weight rather than hidden.
	 */
	let { data } = $props();

	const { company, attempts } = data;

	// Grouped by run, newest first, so the history reads as passes rather than
	// as one undifferentiated list.
	const byRun = [];
	for (const attempt of attempts) {
		const key = attempt.run_id ?? 0;
		let bucket = byRun.find((b) => b.runId === key);
		if (!bucket) {
			bucket = { runId: key, attempts: [] };
			byRun.push(bucket);
		}
		bucket.attempts.push(attempt);
	}
	byRun.sort((a, b) => b.runId - a.runId);
</script>

<svelte:head><title>{company.name}</title></svelte:head>

<p class="back"><a href="/companies">← All companies</a></p>

<h1>{company.name}</h1>
<p class="slug">{company.slug}</p>

<dl>
	<dt>Index</dt>
	<dd>{company.index_membership ?? '—'}</dd>

	<dt>Industry</dt>
	<dd>{company.industry ?? '—'}{company.gics_sub_industry ? ` · ${company.gics_sub_industry}` : ''}</dd>

	<dt>Headquarters</dt>
	<dd>{company.headquarters_location ?? '—'}</dd>

	<dt>Website</dt>
	<dd>
		{#if company.website}
			<a href={company.website} rel="noopener noreferrer" target="_blank">{company.website}</a>
			{#if company.website_source}<span class="slug"> via {company.website_source}</span>{/if}
		{:else}
			<span class="missing">not resolved</span>
		{/if}
	</dd>

	<dt>Careers site</dt>
	<dd>
		{#if company.career_site_url}
			<a href={company.career_site_url} rel="noopener noreferrer" target="_blank">
				{company.career_site_url}
			</a>
			{#if company.career_site_source}<span class="slug"> via {company.career_site_source}</span>{/if}
			{#if company.career_site_title}<div class="slug">page title: {company.career_site_title}</div>{/if}
		{:else if company.attempt_count > 0}
			<span class="missing">looked, none found ({company.attempt_count} attempts)</span>
		{:else}
			<span class="missing">never looked at</span>
		{/if}
	</dd>
</dl>

<h2>Resolution trail</h2>
{#if attempts.length === 0}
	<p class="missing">No attempts recorded.</p>
{:else}
	{#each byRun as run}
		<h3>Run {run.runId === 0 ? 'unknown' : run.runId}</h3>
		<table>
			<thead>
				<tr>
					<th>#</th>
					<th>State</th>
					<th>Source</th>
					<th>Candidate</th>
					<th>Final</th>
					<th>HTTP</th>
					<th>Reason</th>
				</tr>
			</thead>
			<tbody>
				{#each run.attempts as attempt (attempt.id)}
					<tr>
						<td>{attempt.attempt_index}</td>
						<td class={attempt.validation_status}>{attempt.validation_status}</td>
						<td>{attempt.source}</td>
						<td>
							{#if attempt.candidate_url}
								<a href={attempt.candidate_url} rel="noopener noreferrer" target="_blank">
									{attempt.candidate_url.replace(/^https?:\/\//, '')}
								</a>
							{:else}
								—
							{/if}
							{#if attempt.title}<div class="slug">{attempt.title}</div>{/if}
						</td>
						<td>
							{#if attempt.final_url && attempt.final_url !== attempt.candidate_url}
								<a href={attempt.final_url} rel="noopener noreferrer" target="_blank">
									{attempt.final_url.replace(/^https?:\/\//, '')}
								</a>
							{:else}
								—
							{/if}
						</td>
						<td>{attempt.http_status ?? '—'}</td>
						<td>
							{#if attempt.rejection_reason}
								<code>{attempt.rejection_reason}</code>
							{:else}
								—
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/each}
{/if}

<style>
	.back {
		margin-top: 1.5rem;
	}
	h1 {
		font-size: 1.5rem;
		margin: 0.5rem 0 0;
	}
	h2 {
		font-size: 1.15rem;
		margin-top: 2rem;
	}
	h3 {
		font-size: 0.95rem;
		color: #5b6169;
		margin: 1.25rem 0 0.35rem;
	}
	.slug {
		color: #7a8089;
		font-size: 0.82rem;
	}
	.missing {
		color: #a3600a;
	}
	dl {
		display: grid;
		grid-template-columns: 12rem 1fr;
		gap: 0.35rem 1rem;
		margin: 1.25rem 0;
	}
	dt {
		color: #5b6169;
	}
	dd {
		margin: 0;
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.85rem;
	}
	th {
		text-align: left;
		border-bottom: 2px solid #d9dce1;
		padding: 0.35rem 0.5rem;
	}
	td {
		border-bottom: 1px solid #eceef1;
		padding: 0.4rem 0.5rem;
		vertical-align: top;
	}
	td.accepted {
		color: #146c2e;
	}
	td.rejected,
	td.error {
		color: #a3200a;
	}
	code {
		background: #f2f3f5;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
	}
</style>
