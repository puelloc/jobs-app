<script>
	import { page } from '$app/state';

	const heading = $derived(page.status === 404 ? 'Page not found' : 'Something went wrong');
	const message = $derived(page.error?.message ?? 'Unexpected error');
</script>

<svelte:head>
	<title>{heading}</title>
</svelte:head>

<main>
	<h1>{heading}</h1>
	<p class="message">{message}</p>
	<p><a href="/">← Back to runs</a></p>
</main>

<style>
	main {
		max-width: 600px;
		margin: 0 auto;
		padding: 3rem 1.5rem;
	}

	h1 {
		margin: 0;
		font-size: 1.4rem;
	}

	.message {
		margin: 0.75rem 0 1.5rem;
		color: #4a4f55;
	}
</style>
