<script>
	import { formatTraceAction, formatUtc } from '$lib/format.js';

	/**
	 * Renders one agent trace: the browser-use step events (thinking, next goal, actions) followed
	 * by the terminal done event (success + final result). Shared by the run detail page and the job
	 * detail page, where it answers "why was this posting deemed a match?".
	 *
	 * A `resolution` event is written by the scrape itself when it reused a cached listings URL, so
	 * the trace records that no agent ran rather than rendering as empty.
	 * @type {{ events?: any[], present?: boolean }}
	 */
	let { events = [], present = false } = $props();
</script>

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
							{#each ev.actions as action, ai (ai)}<code>{formatTraceAction(action)}</code>{/each}
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
			{:else if ev.event === 'resolution'}
				<li class="resolution">
					<span class="badge badge-cached">Cached</span>
					<span class="steps">
						Listings URL reused{ev.resolved_at ? ` (resolved ${formatUtc(ev.resolved_at)})` : ''}
						— the agent did not run this time.
					</span>
					{#if ev.listings_url}
						<p class="final">
							<a href={ev.listings_url} target="_blank" rel="noreferrer">{ev.listings_url}</a>
						</p>
					{/if}
				</li>
			{/if}
		{/each}
	</ol>
{/if}

<style>
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

	.badge-cached {
		color: #1a56c4;
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
</style>
