# jobs-web

Read-only SvelteKit dashboard over the Go API. **One screen: the runs dashboard at `/`**, which
renders the status and history of every `scrape_runs` row — running runs first, then finished ones —
with a count of active runs.

There is no write path, no auth, and no client-side fetching on first load: the page is
server-rendered, and only then does the browser start polling.

## Prerequisites

- Node 18+ (developed on Node 24) and npm.
- The Go API running on `http://127.0.0.1:8080`, against the database the rest of the project uses:

  ```
  DB_PATH=./jobs.db go run ./cmd/server
  ```

  The API is optional at start-up: with it stopped the app renders an error page rather than hanging
  or showing a blank screen.

## Run

```
cd web
npm install
npm run dev
```

Then open http://localhost:5173/.

`npm run build` and `npm run preview` exist but are not used yet: this is a dev-only setup and no
deployment adapter target is configured.

## How it talks to the API

- Every request goes to a same-origin `/api/*` path.
- `vite.config.js` proxies `/api` to `http://127.0.0.1:8080` in dev, so no CORS configuration is
  needed anywhere and no component hardcodes the Go host.
- `src/lib/api.js` is the only module that fetches. It currently exports one function, `getRuns`.
- The first paint is server-rendered: `+page.js` calls `getRuns` during SSR, so the initial HTML
  already contains the runs. `+page.svelte` then re-runs that same load on a 5-second interval via
  `invalidate('data:runs')`, so a run that is in flight moves from "running" to its result without a
  manual refresh.
- During SSR the request is addressed to the dev server's own origin (`url.origin`) instead of a bare
  relative path, because SvelteKit's own `event.fetch` resolves same-origin requests through this
  app's router, which has no `/api` route. Addressing the origin keeps one code path for both the
  server render and the browser.

## The API serves more than this screen

The Go API exposes job, company and run endpoints; the dashboard uses only the last one. The rest
exist for the data model and for whatever screen comes next — they are not dead code, but nothing in
this app calls them yet:

| Endpoint | Used by the UI |
| --- | --- |
| `GET /api/runs` | **yes** — the dashboard |
| `GET /api/jobs`, `GET /api/jobs/{id}` | no |
| `GET /api/companies`, `GET /api/companies/{id}`, `GET /api/companies/churn` | no |

## Layout

```
src/
  app.html
  lib/
    api.js            fetch wrapper; throws { status, code, message }; exports getRuns
    format.js         pure formatting helpers (formatUtc, formatRunStatus, formatDuration)
  routes/
    +layout.svelte    the shell: top bar, brand, nav
    +page.svelte      runs dashboard
    +page.js          server-side load of GET /api/runs?limit=100&offset=0, plus the poll
    +error.svelte     error route
```

## Notes

- Svelte 5 runes mode is forced on in `svelte.config.js` (`compilerOptions.runes: true`), so legacy
  syntax is a compile error.
- A run's `status` is the whole point of the screen: `running` means the row was written before the
  network was touched and has not been finished yet, so a run stuck in `running` is a crash or a kill
  rather than a quiet success.
- `docs/ui-design.md` is the **v1 design for a jobs list/detail viewer that no longer exists** — that
  UI was replaced by this dashboard. It is kept as a design record, not as a description of the app.
