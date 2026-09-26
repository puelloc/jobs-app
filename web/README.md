# jobs-web

Read-only SvelteKit viewer for the scraped job listings. It consumes the Go API
(`GET /api/jobs`, `GET /api/jobs/{id}`) and renders two screens: a list and a
job detail page. There is no write path, no auth, and no client-side fetching on
first load — both routes are server-rendered.

## Prerequisites

- Node 18+ (developed on Node 24) and npm.
- The Go API running on `http://127.0.0.1:8080`:

  ```
  DB_PATH=./jobs.db go run ./cmd/server
  ```

  The API is optional at start-up: with it stopped the app renders an error page
  rather than hanging or showing a blank screen.

## Run

```
cd web
npm install
npm run dev
```

Then open http://localhost:5173/.

`npm run build` and `npm run preview` exist but are not used yet: this is a
dev-only setup and no deployment adapter target is configured.

## How it talks to the API

- Every request goes to a same-origin `/api/*` path.
- `vite.config.js` proxies `/api` to `http://127.0.0.1:8080` in dev, so no CORS
  configuration is needed anywhere and no component hardcodes the Go host.
- `src/lib/api.js` is the only module that fetches. It is imported by the
  `load()` functions and nowhere else.
- During SSR the request is addressed to the dev server's own origin
  (`url.origin`) instead of a bare relative path, because SvelteKit's own
  `event.fetch` resolves same-origin requests through this app's router, which
  has no `/api` route. Addressing the origin keeps one code path for both the
  server render and the browser.

## Layout

```
src/
  app.html
  lib/
    api.js            fetch wrapper; throws { status, code, message }
    format.js         pure formatting helpers (money, dates, relative time, labels)
    JobCard.svelte    one list row
  routes/
    +layout.svelte    minimal global styles, renders children
    +page.svelte      list view
    +page.js          server-side load of GET /api/jobs?limit=25&offset=0
    +error.svelte     404 (job not found / page not found) and 500
    jobs/[id]/
      +page.svelte    detail view
      +page.js        server-side load of GET /api/jobs/{id}
```

## Notes

- Svelte 5 runes mode is forced on in `svelte.config.js`
  (`compilerOptions.runes: true`), so legacy syntax is a compile error.
- Descriptions are rendered as escaped text inside a `<pre>` block. The payload
  may contain HTML and v1 ships no sanitizer, so `{@html}` is never used.
- `status` is not a clock: a job is `closed` because the most recent scrape did
  not see it, not because time passed. The detail page's "last seen N days ago"
  is a freshness label for when it was last observed.
