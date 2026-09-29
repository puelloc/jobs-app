<script>
	import { goto, invalidate } from '$app/navigation';
	import { poll } from '$lib/poll.js';
	import { getRuns, postJob, postPauseRun, postResumeRun, postStopRun } from '$lib/api.js';
	import { formatDuration, formatRunStatus, formatUtc } from '$lib/format.js';

	let { data } = $props();

	// load() owns the first page; "load more" appends further pages client-side. The local copy is
	// re-synced whenever load() runs again (a poll or a search), so new runs land at the top without
	// losing the user's place while paging.
	let allRuns = $state(data.runs ?? []);
	let offset = $state(data.offset + (data.runs?.length ?? 0));
	let loadingMore = $state(false);
	let loadError = $state('');

	$effect(() => {
		allRuns = data.runs ?? [];
		offset = data.offset + (data.runs?.length ?? 0);
	});

	const total = $derived(data.total ?? 0);
	const hasMore = $derived(offset < total);
	const running = $derived(allRuns.filter((r) => r.status === 'running'));
	const history = $derived(allRuns.filter((r) => r.status !== 'running'));

	async function loadMore() {
		loadingMore = true;
		loadError = '';
		try {
			const page = await getRuns({ limit: data.limit, offset, q: data.q });
			allRuns = [...allRuns, ...(page.runs ?? [])];
			offset += page.runs?.length ?? 0;
		} catch (failure) {
			loadError = failure?.message ?? 'Could not load more runs';
		} finally {
			loadingMore = false;
		}
	}

	const jobs = [
		{ name: 'sp1500', label: 'Bootstrap companies' },
		{ name: 'resolve', label: 'Resolve career sites' },
		{ name: 'validate', label: 'Validate career sites' },
		{ name: 'classify', label: 'Classify vendors' },
		{ name: 'scraper', label: 'Refresh RemoteOK' }
	];

	let pending = $state('');
	let triggerError = $state('');
	let warning = $state('');
	let stopping = $state('');

	// The full scrape sweep's options. Kept local so a re-run can skip companies that already
	// worked, resume from a point, or stop after a run of failures — all from the UI, no CLI.
	let sweepSkipOk = $state(false);
	let sweepSkipTraced = $state(false);
	let sweepFromSlug = $state('');
	let sweepStopAfter = $state('');

	async function stopRun(id) {
		stopping = String(id);
		try {
			await postStopRun(id);
			await invalidate('data:runs');
		} catch (failure) {
			triggerError = failure?.message ?? 'Could not stop the run';
		} finally {
			stopping = '';
		}
	}

	let pausedRun = $state('');

	async function togglePause(id) {
		const idStr = String(id);
		try {
			if (pausedRun === idStr) {
				await postResumeRun(id);
				pausedRun = '';
			} else {
				await postPauseRun(id);
				pausedRun = idStr;
			}
			await invalidate('data:runs');
		} catch (failure) {
			triggerError = failure?.message ?? 'Could not pause/resume the run';
		}
	}

	async function trigger(name) {
		pending = name;
		triggerError = '';
		try {
			const res = await postJob(name);
			// Self-tracking jobs return run_id 0 (their own run appears on the dashboard);
			// server-tracked jobs return the run to watch directly.
			await goto(res.run_id ? `/runs/${res.run_id}` : '/');
		} catch (failure) {
			// A 409 means a job is already running: surface it as a popup, not an inline note.
			if (failure?.code === 'conflict') {
				warning = failure.message;
			} else {
				triggerError = failure?.message ?? `Could not start ${name}`;
			}
			pending = '';
		}
	}

	async function triggerSweep() {
		pending = 'batch';
		triggerError = '';
		try {
			const res = await postJob('batch', {
				skip_ok: sweepSkipOk,
				skip_traced: sweepSkipTraced,
				from_slug: sweepFromSlug.trim() || undefined,
				stop_after_failures: Number(sweepStopAfter) || 0
			});
			await goto(res.run_id ? `/runs/${res.run_id}` : '/');
		} catch (failure) {
			if (failure?.code === 'conflict') {
				warning = failure.message;
			} else {
				triggerError = failure?.message ?? 'Could not start the sweep';
			}
			pending = '';
		}
	}

	// One-click resume: continue from the slug the last sweep left as its resume point.
	async function resumeSweep() {
		if (!data.sweep?.present) return;
		pending = 'batch';
		triggerError = '';
		try {
			const res = await postJob('batch', { from_slug: data.sweep.slug });
			await goto(res.run_id ? `/runs/${res.run_id}` : '/');
		} catch (failure) {
			if (failure?.code === 'conflict') {
				warning = failure.message;
			} else {
				triggerError = failure?.message ?? 'Could not resume the sweep';
			}
			pending = '';
		}
	}

	// Poll the load function so in-flight runs and new finishes show up without
	// a manual refresh. invalidate() re-runs load on the client only.
	poll(() => invalidate('data:runs'));
</script>

<svelte:head>
	<title>Runs · Jobs dashboard</title>
	<meta name="description" content="Status and history of scraper and validation runs" />
</svelte:head>

<main>
	<div class="heading">
		<h1>Runs</h1>
		<p class="sub">Scraper and validation job runs · {total} total · refreshes every 5s</p>
	</div>

	<form class="search" method="get" action="/">
		<input
			type="search"
			name="q"
			placeholder="Search platform or status (e.g. batch, error)"
			value={data.q ?? ''}
		/>
		<button type="submit">Search</button>
		{#if data.q}
			<a class="clear" href="/">Clear</a>
		{/if}
	</form>

	<section class="section">
		<h2>Trigger a job</h2>
		<div class="trigger-row">
			{#each jobs as job (job.name)}
				<button
					class="trigger"
					disabled={pending !== ''}
					onclick={() => trigger(job.name)}
				>
					{pending === job.name ? 'Starting…' : job.label}
				</button>
			{/each}
		</div>
		{#if triggerError}
			<p class="error">{triggerError}</p>
		{/if}
	</section>

	<section class="section">
		<h2>Full scrape sweep</h2>
		<p class="sweep-hint">
			Scrapes every classified company, one at a time. Check a box to re-run only the companies
			that still need it — “skip by trace” is the reliable signal that browser-use actually
			ran, since a run can report success even when the agent died before its first step.
		</p>
		<div class="sweep">
			<label class="check">
				<input type="checkbox" bind:checked={sweepSkipOk} />
				Skip companies whose last run succeeded
			</label>
			<label class="check">
				<input type="checkbox" bind:checked={sweepSkipTraced} />
				Skip companies with a browser-use trace
			</label>
			<label class="field">
				<span>Start after slug</span>
				<input
					type="text"
					bind:value={sweepFromSlug}
					placeholder="optional, e.g. abbott-laboratories"
				/>
			</label>
			<label class="field">
				<span>Stop after N failures</span>
				<input type="number" min="0" step="1" bind:value={sweepStopAfter} placeholder="0 = never" />
			</label>
			<button class="trigger" disabled={pending !== ''} onclick={triggerSweep}>
				{pending === 'batch' ? 'Starting…' : 'Run sweep'}
			</button>
			{#if data.sweep?.present}
				<button class="trigger resume" disabled={pending !== ''} onclick={resumeSweep}>
					{pending === 'batch' ? 'Starting…' : `Resume from ${data.sweep.slug}`}
				</button>
			{/if}
		</div>
	</section>

	<section class="section">
		<h2>Running</h2>
		{#if running.length === 0}
			<p class="quiet">Nothing running right now.</p>
		{:else}
			<ul class="run-list">
				{#each running as run (run.id)}
					<li class="run run-running">
						<span class="dot" aria-hidden="true"></span>
						<div class="run-main">
							<span class="platform">{run.platform}</span>
							{#if pausedRun === String(run.id)}
								<span class="badge badge-paused">Paused</span>
							{:else}
								<span class="badge badge-running">Running</span>
							{/if}
							<button
								class="pause"
								disabled={stopping !== ''}
								onclick={() => togglePause(run.id)}
							>
								{pausedRun === String(run.id) ? 'resume' : 'pause'}
							</button>
							<button
								class="stop"
								disabled={stopping !== ''}
								onclick={() => stopRun(run.id)}
							>
								{stopping === String(run.id) ? 'Stopping…' : 'stop'}
							</button>
							<a class="view" href={`/runs/${run.id}`}>watch →</a>
						</div>
						<div class="meta">started {formatUtc(run.started_at)} · #{run.id}</div>
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	<section class="section">
		<h2>History</h2>
		{#if history.length === 0}
			<p class="empty">No finished runs yet. Run a job, then this page will pick it up.</p>
		{:else}
			<ul class="run-list">
				{#each history as run (run.id)}
					<li class="run">
						<div class="run-main">
							<span class="platform">{run.platform}</span>
							<span class="badge badge-{run.status}">{formatRunStatus(run.status)}</span>
							<a class="view" href={`/runs/${run.id}`}>view →</a>
						</div>
						<div class="meta">
							<span>#{run.id}</span>
							<span>{formatUtc(run.started_at)}</span>
							{#if run.finished_at}
								<span>{formatDuration(run.started_at, run.finished_at)}</span>
							{/if}
							<span class="counts">
								{run.items_found} found · {run.items_inserted} in · {run.items_updated} up
								{#if run.items_wrong || run.items_unverifiable}
									· {run.items_wrong} wrong · {run.items_unverifiable} unverifiable
								{/if}
								{#if run.dry_run}· dry run{/if}
							</span>
						</div>
						{#if run.error_text}
							<p class="error">{run.error_text}</p>
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
	</section>
</main>

{#if warning}
	<div class="modal-backdrop" role="presentation" onclick={() => (warning = '')}>
		<div
			class="modal"
			role="alertdialog"
			aria-modal="true"
			aria-label="Job already running"
			onclick={(e) => e.stopPropagation()}
		>
			<p class="modal-title">⚠️ A job is already running</p>
			<p class="modal-body">{warning}</p>
			<button class="modal-close" onclick={() => (warning = '')}>OK</button>
		</div>
	</div>
{/if}

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

	.search {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 1.25rem;
	}

	.search input {
		font: inherit;
		padding: 0.4rem 0.6rem;
		border: 1px solid #d4d9e0;
		border-radius: 6px;
		background: #ffffff;
		color: #171b21;
		width: 22rem;
		max-width: 100%;
	}

	.search button {
		font: inherit;
		padding: 0.4rem 0.9rem;
		border: 1px solid #1a56c4;
		border-radius: 6px;
		background: #1a56c4;
		color: #ffffff;
		cursor: pointer;
	}

	.search .clear {
		font-size: 0.85rem;
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

	.quiet,
	.empty {
		margin: 0;
		color: #6b7178;
	}

	.empty {
		margin-top: 0.25rem;
	}

	.run-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.run {
		position: relative;
		padding: 0.7rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.run-running {
		border-left: 3px solid #1a7f37;
	}

	.run-main {
		display: flex;
		align-items: center;
		gap: 0.6rem;
	}

	.platform {
		font-weight: 600;
	}

	.view {
		margin-left: auto;
		color: #6b7178;
		font-size: 0.82rem;
		white-space: nowrap;
	}

	.stop {
		margin-left: 0.25rem;
		padding: 0.05rem 0.5rem;
		border: 1px solid #c62828;
		border-radius: 6px;
		background: #ffffff;
		color: #c62828;
		font-size: 0.75rem;
		font-weight: 600;
		cursor: pointer;
	}

	.stop:disabled {
		border-color: #e4e7ec;
		color: #8a9099;
		cursor: not-allowed;
	}

	.pause {
		margin-left: 0.25rem;
		padding: 0.05rem 0.5rem;
		border: 1px solid #9a6b00;
		border-radius: 6px;
		background: #ffffff;
		color: #9a6b00;
		font-size: 0.75rem;
		font-weight: 600;
		cursor: pointer;
	}

	.pause:disabled {
		border-color: #e4e7ec;
		color: #8a9099;
		cursor: not-allowed;
	}

	.trigger-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.trigger {
		padding: 0.4rem 0.8rem;
		border: 1px solid #1a7f37;
		border-radius: 6px;
		background: #1a7f37;
		color: #ffffff;
		font-size: 0.85rem;
		font-weight: 600;
		cursor: pointer;
	}

	.trigger:disabled {
		background: #e4e7ec;
		border-color: #e4e7ec;
		color: #8a9099;
		cursor: not-allowed;
	}

	.trigger.resume {
		background: #1a56c4;
		border-color: #1a56c4;
	}

	.sweep-hint {
		margin: 0 0 0.6rem;
		color: #6b7178;
		font-size: 0.82rem;
	}

	.sweep {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: 0.6rem 1rem;
		padding: 0.85rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.sweep .check {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.82rem;
		align-self: center;
	}

	.sweep .field {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
	}

	.sweep .field span {
		font-size: 0.72rem;
		font-weight: 650;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: #6b7178;
	}

	.sweep .field input {
		font: inherit;
		padding: 0.35rem 0.5rem;
		border: 1px solid #d4d9e0;
		border-radius: 6px;
		background: #ffffff;
		color: #171b21;
	}

	.sweep .field input[type='text'] {
		width: 16rem;
	}

	.sweep .field input[type='number'] {
		width: 8rem;
	}

	.error {
		margin: 0.6rem 0 0;
		font-size: 0.85rem;
		color: #c62828;
	}

	.badge {
		padding: 0.05rem 0.5rem;
		border-radius: 999px;
		border: 1px solid currentColor;
		font-size: 0.75rem;
		font-weight: 650;
		letter-spacing: 0.02em;
	}

	.badge-ok {
		color: #1a7f37;
	}

	.badge-error {
		color: #c62828;
	}

	.badge-running {
		color: #1a7f37;
	}

	.badge-paused {
		color: #9a6b00;
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.25rem 0.9rem;
		margin-top: 0.3rem;
		font-size: 0.82rem;
		color: #6b7178;
		font-variant-numeric: tabular-nums;
	}

	.counts {
		color: #8a9099;
	}

	.error {
		margin: 0.4rem 0 0;
		font-size: 0.82rem;
		color: #c62828;
		white-space: pre-wrap;
		word-break: break-word;
	}

	.dot {
		position: absolute;
		left: -0.4rem;
		top: 50%;
		width: 0.6rem;
		height: 0.6rem;
		margin-top: -0.3rem;
		border-radius: 50%;
		background: #2da44e;
		animation: pulse 1.4s ease-in-out infinite;
	}

	@keyframes pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.35;
		}
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

	.modal-backdrop {
		position: fixed;
		inset: 0;
		background: rgba(23, 27, 33, 0.45);
		display: flex;
		align-items: center;
		justify-content: center;
		z-index: 100;
	}

	.modal {
		width: min(26rem, calc(100vw - 2rem));
		padding: 1.25rem 1.25rem 1rem;
		background: #ffffff;
		border-radius: 10px;
		box-shadow: 0 12px 40px rgba(0, 0, 0, 0.25);
	}

	.modal-title {
		margin: 0 0 0.4rem;
		font-size: 1.05rem;
		font-weight: 650;
	}

	.modal-body {
		margin: 0 0 1rem;
		color: #6b7178;
	}

	.modal-close {
		display: block;
		margin-left: auto;
		padding: 0.4rem 1rem;
		border: 1px solid #1a7f37;
		border-radius: 6px;
		background: #1a7f37;
		color: #ffffff;
		font-size: 0.85rem;
		font-weight: 600;
		cursor: pointer;
	}
</style>
