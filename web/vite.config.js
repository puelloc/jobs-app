import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Dev only: /api/* is proxied to the Go API so the app can fetch same-origin
// paths and no CORS configuration is needed anywhere. The app never hardcodes
// the Go host; only this proxy knows it.
export default defineConfig({
	plugins: [sveltekit()],
	server: {
		proxy: {
			'/api': {
				target: 'http://127.0.0.1:8080',
				changeOrigin: true
			}
		}
	}
});
