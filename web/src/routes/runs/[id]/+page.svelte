<script>
	import { invalidate } from '$app/navigation';
	import { onMount } from 'svelte';
	import {
		formatDuration,
		formatRunStatus,
		formatTraceAction,
		formatUtc
	} from '$lib/format.js';

	let { data } = $props();

	const run = $derived(data.run);
	const events = $derived(data.trace?.events ?? []);
	const present = $derived(data.trace?.present ?? false);
	const log = $derived(data.log?.log ?? '');
	const logPresent = $derived(data.log?.present ?? false);
	const isAgentRun = $derived(run.platform === 'career_listings');

	// Poll the load function so live agent steps and a status change show up
	// without a manual refresh. invalidate() re-runs load on the client only.
	onMount(() => {
		const timer = setInterval(() => invalidate('data:run'), 3000);
		return () => clearInterval(timer);
	});
</script>

<svelte:head>
	<title>Run #{run.id} · Jobs dashboard</title>
	<meta name="description" content={`Status and trace for run ${run.id}`} />
</svelte:head>

<main>
	<div class="heading">
		<h1>Run #{run.id}</h1>
		<span class="badge badge-{run.status}">{formatRunStatus(run.status)}</span>
	</div>
	<p class="sub">
		<span>{run.platform}</span>
		<span>started {formatUtc(run.started_at)}</span>
		{#if run.finished_at}
			<span>{formatDuration(run.started_at, run.finished_at)}</span>
		{/if}
		{#if run.dry_run}<span>dry run</span>{/if}
	</p>

	<section class="section">
		<h2>Result</h2>
		<dl class="facts">
			<div>
				<dt>Status</dt>
				<dd>{formatRunStatus(run.status)}</dd>
			</div>
			<div>
				<dt>Found</dt>
				<dd>{run.items_found}</dd>
			</div>
			<div>
				<dt>Inserted</dt>
				<dd>{run.items_inserted}</dd>
			</div>
			<div>
				<dt>Updated</dt>
				<dd>{run.items_updated}</dd>
			</div>
			{#if run.items_wrong || run.items_unverifiable}
				<div>
					<dt>Wrong / unverifiable</dt>
					<dd>{run.items_wrong} / {run.items_unverifiable}</dd>
				</div>
			{/if}
			{#if run.error_text}
				<div>
					<dt>Error</dt>
					<dd class="error">{run.error_text}</dd>
				</div>
			{/if}
		</dl>
	</section>

	<section class="section">
		{#if isAgentRun}
			<h2>Agent trace</h2>
			{#if !present && events.length === 0}
				<p class="quiet">No trace yet — the agent has not written its first step.</p>
			{:else}
				<ol class="trace">
					{#each events as ev, i (i)}
						{#if ev.event === 'step'}
							<li class="step">
								<div class="step-head">
									<span class="step-num">Step {ev.step}</span>
									{#if ev.url}
										<a href={ev.url} target="_blank" rel="noreferrer">{ev.url}</a>
									{/if}
								</div>
								{#if ev.next_goal}
									<div class="field"><span class="label">next goal</span><p>{ev.next_goal}</p></div>
								{/if}
								{#if ev.thinking}
									<div class="field"><span class="label">thinking</span><p>{ev.thinking}</p></div>
								{/if}
								{#if ev.actions?.length}
									<div class="actions">
										{#each ev.actions as action}<code>{formatTraceAction(action)}</code>{/each}
									</div>
								{/if}
							</li>
						{:else if ev.event === 'done'}
							<li class="done">
								<span class="badge badge-{ev.success ? 'ok' : 'error'}">
									{ev.success ? 'Done' : 'Failed'}
								</span>
								<span class="steps">{ev.steps} steps</span>
								{#if ev.final_result}<p class="final">{ev.final_result}</p>{/if}
							</li>
						{/if}
					{/each}
				</ol>
			{/if}
		{:else}
			<h2>Output</h2>
			{#if !logPresent || !log}
				<p class="quiet">No output yet.</p>
			{:else}
				<pre class="log">{log}</pre>
			{/if}
		{/if}
	</section>

	<p><a href="/" class="back">← Back to runs</a></p>
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

	.quiet {
		margin: 0;
		color: #6b7178;
	}

	.trace {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.step,
	.done {
		padding: 0.7rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
	}

	.step-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.6rem;
		word-break: break-all;
	}

	.step-num {
		font-weight: 650;
		font-variant-numeric: tabular-nums;
	}

	.field {
		margin-top: 0.5rem;
	}

	.field .label {
		display: block;
		font-size: 0.72rem;
		font-weight: 650;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: #8a9099;
	}

	.field p {
		margin: 0.15rem 0 0;
		white-space: pre-wrap;
		word-break: break-word;
		font-size: 0.88rem;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem 0.5rem;
		margin-top: 0.5rem;
	}

	.actions code {
		padding: 0.1rem 0.4rem;
		background: #eef0f3;
		border-radius: 4px;
		font-size: 0.8rem;
	}

	.steps {
		color: #6b7178;
		font-size: 0.85rem;
		margin-left: 0.4rem;
	}

	.final {
		margin: 0.5rem 0 0;
		white-space: pre-wrap;
		word-break: break-word;
		font-size: 0.88rem;
	}

	.log {
		margin: 0;
		padding: 0.7rem 0.9rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
		white-space: pre-wrap;
		word-break: break-word;
		font-size: 0.8rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
		max-height: 40rem;
		overflow: auto;
	}

	.error {
		color: #c62828;
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
