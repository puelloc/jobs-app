<script>
	let { children } = $props();
</script>

{@render children()}

<style>
	/* The only global styles in the app: a minimal reset and the base type. */
	:global(html) {
		box-sizing: border-box;
	}

	:global(*, *::before, *::after) {
		box-sizing: inherit;
	}

	:global(body) {
		margin: 0;
		background: #fbfbfc;
		color: #1c1e21;
		font-family:
			-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
		line-height: 1.45;
	}

	:global(a) {
		color: #1552c4;
	}
</style>
