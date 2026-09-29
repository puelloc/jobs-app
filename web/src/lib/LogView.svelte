<script>
	// A paginated, newest-first view of a plain-text log. Logs are appended in chronological order,
	// so the text is split into lines and reversed: the most recent line is at the top. "Show older"
	// reveals the next page. `visible` is preserved across polls, so new lines land at the top without
	// resetting the user's place.
	const PAGE = 200;

	/** @type {{ text?: string }} */
	let { text = '' } = $props();

	const lines = $derived(text ? text.replace(/\n+$/, '').split('\n') : []);
	const ordered = $derived([...lines].reverse());

	let visible = $state(PAGE);
	const shown = $derived(ordered.slice(0, visible));
	const remaining = $derived(ordered.length - visible);
</script>

{#if ordered.length === 0}
	<p class="quiet">No output yet.</p>
{:else}
	<pre class="log">{shown.join('\n')}</pre>
	{#if remaining > 0}
		<div class="controls">
			<button onclick={() => (visible += PAGE)}>Show older ({remaining} more lines)</button>
		</div>
	{/if}
{/if}

<style>
	.quiet {
		margin: 0;
		color: #6b7178;
	}

	.log {
		margin: 0;
		padding: 0.9rem 1rem;
		background: #ffffff;
		border: 1px solid #e4e7ec;
		border-radius: 8px;
		white-space: pre-wrap;
		word-break: break-word;
		font-size: 0.8rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
		/* Cap the height so a long log does not stretch the page; the shown page scrolls in place. */
		max-height: 60vh;
		overflow-y: auto;
	}

	.controls {
		margin-top: 0.5rem;
	}

	.controls button {
		padding: 0.3rem 0.8rem;
		border: 1px solid #1a56c4;
		border-radius: 6px;
		background: #ffffff;
		color: #1a56c4;
		font-size: 0.82rem;
		font-weight: 600;
		cursor: pointer;
	}
</style>
