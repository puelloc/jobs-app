<script>
	import { goto, invalidate } from '$app/navigation';
	import { onMount } from 'svelte';
	import { postJob, postStopRun } from '$lib/api.js';
	import { formatDuration, formatRunStatus, formatUtc } from '$lib/format.js';

	let { data } = $props();

	// load() owns this data: it is replaced wholesale whenever invalidate
	// re-runs it, so it needs no local state and no deep reactivity.
	const runs = $derived(data.runs ?? []);
	const total = $derived(data.total ?? 0);
	const running = $derived(runs.filter((r) => r.status === 'running'));
	const history = $derived(runs.filter((r) => r.status !== 'running'));

	const jobs = [
		{ name: 'sp1500', label: 'Bootstrap companies' },
		{ name: 'resolve', label: 'Resolve career sites' },
		{ name: 'validate', label: 'Validate career sites' },
		{ name: 'classify', label: 'Classify vendors' },
		{ name: 'batch', label: 'Full scrape sweep' },
		{ name: 'scraper', label: 'Refresh RemoteOK' }
	];

	let pending = $state('');
	let triggerError = $state('');
	let warning = $state('');
	let stopping = $state('');

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

	// Poll the load function so in-flight runs and new finishes show up without
	// a manual refresh. invalidate() re-runs load on the client only.
	onMount(() => {
		const timer = setInterval(() => invalidate('data:runs'), 5000);
		return () => clearInterval(timer);
	});
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
							<span class="badge badge-running">Running</span>
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
