<script>
	import { postScrape } from '$lib/api.js';
	import {
		NOT_STATED,
		formatAttemptCount,
		formatIndexMembership,
		formatUtc,
		formatValidationStatus,
		formatVerdict,
		humanize
	} from '$lib/format.js';

	let { data } = $props();

	const company = $derived(data.company);
	const attempts = $derived(data.attempts ?? []);
	const verdict = $derived(company.career_site_url_verdict ?? null);
	const vendor = $derived(data.vendor ?? null);

	let pending = $state(false);
	let startedRun = $state(null);
	let scrapeError = $state('');

	async function triggerScrape() {
		pending = true;
		scrapeError = '';
		startedRun = null;
		try {
			startedRun = await postScrape(company.id);
		} catch (failure) {
			scrapeError = failure?.message ?? 'The scrape could not be started';
		} finally {
			pending = false;
		}
	}
</script>

<svelte:head>
	<title>{company.name} · Jobs dashboard</title>
	<meta name="description" content={`Career-site resolution trail for ${company.name}`} />
</svelte:head>

<main>
	<div class="heading">
		<h1>{company.name}</h1>
		<span class="badge badge-{verdict ?? 'none'}">{formatVerdict(verdict)}</span>
	</div>
	<p class="sub">
		<span class="slug">{company.slug}</span>
		<span>updated {formatUtc(company.updated_at)}</span>
	</p>

	<section class="section">
		<h2>Careers site</h2>
		<dl class="facts">
			<div>
				<dt>Career site URL</dt>
				<dd>
					{#if company.career_site_url}
						<a href={company.career_site_url} target="_blank" rel="noreferrer">
							{company.career_site_url}
						</a>
					{:else}
						{NOT_STATED}
					{/if}
				</dd>
			</div>
			<div>
				<dt>Page title</dt>
				<dd>{company.career_site_title ?? NOT_STATED}</dd>
			</div>
			<div>
				<dt>Source</dt>
				<dd>{company.career_site_source ? humanize(company.career_site_source) : NOT_STATED}</dd>
			</div>
			<div>
				<dt>Validation verdict</dt>
				<dd>{formatVerdict(verdict)}</dd>
			</div>
		</dl>
	</section>

	<section class="section">
		<h2>Scrape</h2>
		{#if !vendor}
			<p class="hint">
				Not classified. Run <code>classify -commit</code> before scraping this company.
			</p>
		{:else}
			<p class="hint">
				Classified vendor: {vendor}. Scrapes remote-US software-engineering postings into
				job_listings.
			</p>
		{/if}
		<button class="scrape" disabled={!vendor || pending} onclick={triggerScrape}>
			{pending ? 'Starting…' : 'Scrape remote-US jobs'}
		</button>
		{#if startedRun}
			<p class="ok">
				Run started — <a href={`/runs/${startedRun.run_id}`}>watch #{startedRun.run_id}</a>.
			</p>
		{/if}
		{#if scrapeError}
			<p class="error">{scrapeError}</p>
		{/if}
	</section>

	<section class="section">
		<h2>Company</h2>
		<dl class="facts">
			<div>
				<dt>Website</dt>
				<dd>
					{#if company.website}
						<a href={company.website} target="_blank" rel="noreferrer">{company.website}</a>
					{:else}
						{NOT_STATED}
					{/if}
				</dd>
			</div>
			<div>
				<dt>Website source</dt>
				<dd>{company.website_source ? humanize(company.website_source) : NOT_STATED}</dd>
			</div>
			<div>
				<dt>Industry</dt>
				<dd>{company.industry ?? NOT_STATED}</dd>
			</div>
			<div>
				<dt>GICS sub-industry</dt>
				<dd>{company.gics_sub_industry ?? NOT_STATED}</dd>
			</div>
			<div>
				<dt>Headquarters</dt>
				<dd>{company.headquarters_location ?? NOT_STATED}</dd>
			</div>
			<div>
				<dt>Index</dt>
				<dd>
					{company.index_membership ? formatIndexMembership(company.index_membership) : NOT_STATED}
				</dd>
			</div>
			<div>
				<dt>Resolution attempts</dt>
				<dd>{formatAttemptCount(company.attempt_count)}</dd>
			</div>
		</dl>
	</section>

	<section class="section">
		<h2>Resolution attempts</h2>
		{#if attempts.length === 0}
			<p class="empty">No resolution attempts recorded.</p>
		{:else}
			<ol class="attempt-list">
				{#each attempts as attempt (attempt.id)}
					<li class="attempt">
						<div class="attempt-main">
							<span class="attempt-index">#{attempt.attempt_index}</span>
							<a href={attempt.candidate_url} target="_blank" rel="noreferrer">
								{attempt.candidate_url}
							</a>
							<span class="badge badge-{attempt.validation_status}">
								{formatValidationStatus(attempt.validation_status)}
							</span>
						</div>
						<div class="meta">
							<span>{humanize(attempt.source)}</span>
							<span>{humanize(attempt.candidate_kind)}</span>
							{#if attempt.http_status != null}
								<span>HTTP {attempt.http_status}</span>
							{/if}
							<span>{formatUtc(attempt.created_at)}</span>
							{#if attempt.run_id != null}
								<span>run #{attempt.run_id}</span>
							{/if}
						</div>
						{#if attempt.final_url && attempt.final_url !== attempt.candidate_url}
							<div class="final">
								<a href={attempt.final_url} target="_blank" rel="noreferrer">
									→ {attempt.final_url}
								</a>
							</div>
						{/if}
						{#if attempt.title}
							<div class="title">{attempt.title}</div>
						{/if}
						{#if attempt.rejection_reason}
							<div class="reason">{humanize(attempt.rejection_reason)}</div>
						{/if}
					</li>
				{/each}
			</ol>
		{/if}
	</section>

	<p><a href="/companies" class="back">← Back to companies</a></p>
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

	.sub {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.9rem;
		margin: 0.4rem 0 1.5rem;
		color: #6b7178;
		font-size: 0.9rem;
	}

	.slug {
		color: #8a9099;
	}

	.badge {
		padding: 0.05rem 0.5rem;
		border-radius: 999px;
		border: 1px solid currentColor;
		font-size: 0.75rem;
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

	.badge-accepted {
		color: #1a7f37;
	}

	.badge-rejected {
		color: #c62828;
	}

	.badge-error {
		color: #9a6b00;
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
		word-break: break-all;
	}

	.attempt-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.attempt {
		padding: 0.7rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.attempt-main {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		word-break: break-all;
	}

	.attempt-index {
		color: #8a9099;
		font-size: 0.82rem;
		font-variant-numeric: tabular-nums;
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.9rem;
		margin-top: 0.3rem;
		font-size: 0.82rem;
		color: #6b7178;
	}

	.final,
	.title,
	.reason {
		margin-top: 0.3rem;
		font-size: 0.85rem;
		word-break: break-all;
	}

	.final {
		color: #6b7178;
	}

	.reason {
		color: #c62828;
	}

	.empty {
		margin: 0;
		color: #6b7178;
	}

	.hint {
		margin: 0 0 0.6rem;
		color: #6b7178;
		font-size: 0.85rem;
	}

	.hint code {
		padding: 0.05rem 0.3rem;
		background: #eef0f3;
		border-radius: 4px;
	}

	.scrape {
		padding: 0.45rem 0.9rem;
		border: 1px solid #1a7f37;
		border-radius: 6px;
		background: #1a7f37;
		color: #ffffff;
		font-size: 0.9rem;
		font-weight: 600;
		cursor: pointer;
	}

	.scrape:disabled {
		background: #e4e7ec;
		border-color: #e4e7ec;
		color: #8a9099;
		cursor: not-allowed;
	}

	.ok {
		margin: 0.6rem 0 0;
		color: #1a7f37;
		font-size: 0.85rem;
	}

	.error {
		margin: 0.6rem 0 0;
		color: #c62828;
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
