<script>
	import { page } from '$app/state';
	import { refreshMs } from '$lib/refresh.js';

	let { children } = $props();

	const path = $derived(page.url.pathname);

	// "Runs" owns the exact root; the directories own their whole subtree.
	function isActive(match) {
		return match === '/' ? path === '/' : path === match || path.startsWith(match + '/');
	}
</script>

<div class="shell">
	<header class="topbar">
		<a href="/" class="brand">Jobs <span>dashboard</span></a>
		<nav class="nav">
			<a
				href="/"
				class:active={isActive('/')}
				aria-current={isActive('/') ? 'page' : undefined}>Runs</a
			>
			<a
				href="/companies"
				class:active={isActive('/companies')}
				aria-current={isActive('/companies') ? 'page' : undefined}>Companies</a
			>
			<a
				href="/jobs"
				class:active={isActive('/jobs')}
				aria-current={isActive('/jobs') ? 'page' : undefined}>Jobs</a
			>
			<a
				href="/logs"
				class:active={isActive('/logs')}
				aria-current={isActive('/logs') ? 'page' : undefined}>Logs</a
			>
		</nav>
		<label class="refresh">
			Refresh
			<select
				value={$refreshMs}
				onchange={(e) => refreshMs.set(Number(e.currentTarget.value))}
			>
				<option value={1000}>1s</option>
				<option value={2000}>2s</option>
				<option value={3000}>3s</option>
				<option value={5000}>5s</option>
				<option value={10000}>10s</option>
				<option value={30000}>30s</option>
			</select>
		</label>
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

	.refresh {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		margin-left: auto;
		color: #c3c9d4;
		font-size: 0.82rem;
	}

	.refresh select {
		padding: 0.15rem 0.35rem;
		border: 1px solid rgba(255, 255, 255, 0.25);
		border-radius: 6px;
		background: rgba(255, 255, 255, 0.1);
		color: #ffffff;
		font-size: 0.82rem;
	}
</style>
