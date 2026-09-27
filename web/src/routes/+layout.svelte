<script>
	let { children } = $props();
</script>

<div class="shell">
	<header class="topbar">
		<a href="/" class="brand">Jobs <span>dashboard</span></a>
		<nav class="nav">
			<a href="/" class="active" aria-current="page">Runs</a>
		</nav>
	</header>
	{@render children()}
</div>

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
		background: #f6f7f9;
		color: #171b21;
		font-family:
			-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
		line-height: 1.45;
	}

	:global(a) {
		color: #1a56c4;
		text-decoration: none;
	}

	:global(a:hover) {
		text-decoration: underline;
	}

	.topbar {
		display: flex;
		align-items: center;
		gap: 1.5rem;
		padding: 0.7rem 1.5rem;
		background: #171b21;
		color: #f6f7f9;
	}

	.brand {
		font-size: 1.05rem;
		font-weight: 650;
		color: #ffffff;
	}

	.brand span {
		color: #8b93a1;
		font-weight: 400;
	}

	.nav {
		display: flex;
		gap: 0.25rem;
	}

	.nav a {
		padding: 0.25rem 0.6rem;
		border-radius: 6px;
		color: #c3c9d4;
	}

	.nav a:hover {
		color: #ffffff;
		text-decoration: none;
	}

	.nav a.active {
		color: #ffffff;
		background: rgba(255, 255, 255, 0.12);
	}
</style>
