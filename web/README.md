# jobs-web

SvelteKit dashboard over the Go API. Six screens:

- **Runs** at `/` — the status and history of every `scrape_runs` row: running runs first, then
  finished ones. This is the control center: the **Trigger a job** buttons (bootstrap / resolve /
  validate / classify / RemoteOK) launch the pipeline, the **Full scrape sweep** panel runs the
  browser-use sweep with skip options and a **"Resume from `<slug>`"** button (shown whenever a
  previous sweep stopped and left a resume point), and each running run gets **pause / resume / stop**
  and a **watch →** link.
- **Run detail** at `/runs/[id]` — one run's status and counters, its output, and (for
  `career_listings` runs) the live agent trace (the `step`/`done` events the browser-use worker
  writes). For self-tracked jobs (resolve/validate/scraper/bootstrap) the output shown is the shared
  server-log tail, since those jobs tee into it rather than a per-run file.
- **Companies** at `/companies` and `/companies/[id]` — a directory of companies and, per company,
  the careers-site resolution trail behind it. The directory shows each company's `career_site_url`
  (linked), its validation verdict (`confirmed`/`wrong`/`unverifiable`), and its attempt count, with
  filters for `resolution`, `index`, and `search`. The detail page shows the company's facts, every
  `url_resolution_attempts` row (accepted and rejected alike), and the **Scrape** button that
  triggers one company's listings scrape (disabled until the company is classified).
- **Jobs** at `/jobs` and `/jobs/[id]` — the scraped job listings, with a per-job page showing the
  description (rendered as escaped plain text, never as HTML, capped to a scrollable box) and the
  listing/application/discovery URLs. When a job is linked to the scrape run that produced it, the
  page also shows a **"Why this job matched"** section with that run's agent trace, so a posting that
  is not really a software-engineering role can be traced to the reasoning that admitted it.
- **Logs** at `/logs` — the tail of the Go API server's own log, where self-tracked jobs stream their
  per-company progress. Shown **newest-first and paginated** (200 lines per page, "Show older" to page
  back).

The write paths are the trigger buttons (`POST /api/pipeline/{name}`), the per-company scrape
(`POST /api/companies/{id}/scrape`), and run control (`POST /api/runs/{id}/stop|pause|resume`).
There is no auth. Every page is server-rendered first, then kept live by polling.

## Refresh frequency

The top bar has a **Refresh** dropdown (1s / 2s / 3s / 5s / 10s / 30s). It drives every live poll
(runs dashboard, run detail, server log) and is persisted to `localStorage`, so it survives reloads
and redeploys. Changing it re-arms every timer immediately.

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

`npm run build` builds a standalone Node server with `@sveltejs/adapter-node`
(`web/build/index.js`) — this is what the `ui` Docker service runs. `npm run preview` serves that
build locally.

## How it talks to the API

- Every request goes to a same-origin `/api/*` path.
- In dev, `vite.config.js` proxies `/api` to `http://127.0.0.1:8080`. In production there is no Vite:
  `src/routes/api/[...path]/+server.js` proxies `/api/*` to the Go API (`JOBS_API`, default
  `http://127.0.0.1:8080`), so no CORS configuration is needed anywhere and no component knows the Go
  host.
- `src/lib/api.js` is the only module that fetches. During SSR it uses SvelteKit's `event.fetch` with
  a relative path (routed through the proxy); in the browser it uses the global `fetch` (same-origin).
- The first paint is server-rendered: each `+page.js` calls its `get*` function during SSR, so the
  initial HTML already contains the data. `+page.svelte` then keeps it live via `$lib/poll.js`, which
  re-runs the load on the chosen interval through `invalidate(...)`.

## The API the screens use

| Endpoint | Used by the UI |
| --- | --- |
| `GET /api/runs` | **yes** — the runs dashboard (`/`) |
| `GET /api/runs/{id}` | **yes** — a run's detail (`/runs/{id}`) |
| `POST /api/runs/{id}/stop` | **yes** — the stop button |
| `POST /api/runs/{id}/pause` / `resume` | **yes** — the pause/resume button |
| `POST /api/pipeline/{name}` | **yes** — the trigger buttons (`sp1500`, `resolve`, `validate`, `classify`, `batch`, `scraper`); `batch` also accepts a JSON body with the sweep options (`skip_ok`, `skip_traced`, `from_slug`, `stop_after_failures`, `limit`) |
| `GET /api/pipeline/{id}/log` | **yes** — a run's per-run output |
| `GET /api/logs/server` | **yes** — the Logs page, and self-tracked runs' output |
| `GET /api/sweep/position` | **yes** — the sweep panel's "Resume from `<slug>`" button |
| `GET /api/traces/{id}` | **yes** — a `career_listings` run's live agent trace |
| `POST /api/companies/{id}/scrape` | **yes** — a company's Scrape button |
| `GET /api/companies` | **yes** — the company directory (`/companies`) |
| `GET /api/companies/{id}` | **yes** — a company + its resolution trail (`/companies/{id}`) |
| `GET /api/companies/churn` | no — no screen yet |
| `GET /api/jobs` | **yes** — the jobs list (`/jobs`) |
| `GET /api/jobs/{id}` | **yes** — a job's detail (`/jobs/{id}`) |

## Layout

```
src/
  hooks.server.js    logs server errors so `docker compose logs ui` shows them
  app.html
  lib/
    api.js           the only fetch module; throws { status, code, message }
    format.js        pure formatting helpers
    refresh.js       the refresh-frequency store (localStorage-backed)
    poll.js          poll(fn) — re-arms an interval when the refresh store changes
    LogView.svelte   paginated, newest-first, scrollable log view
    TraceView.svelte one agent trace (steps + done); shared by the run and job pages
  routes/
    +layout.svelte   the shell: top bar, nav (Runs / Companies / Jobs / Logs), Refresh dropdown
    +page.svelte     runs dashboard: trigger buttons + sweep options + running/history runs with pause/resume/stop
    +page.js         server-side load of GET /api/runs
    runs/[id]/       run detail: status, output, agent trace (career_listings)
    companies/       directory; [id]/ = one company + trail + Scrape button
    jobs/            listings list; [id]/ = one listing
    logs/            the server log, live
    api/[...path]/+server.js   the production /api proxy to the Go API
    +error.svelte    error route
```

## Notes

- Svelte 5 runes mode is forced on in `svelte.config.js` (`compilerOptions.runes: true`), so legacy
  syntax is a compile error.
- A run's `status` is the whole point of the screen: `running` means the row was written before the
  network was touched and has not been finished yet. A run stuck in `running` is a crash or a kill —
  except that on server start any stale `running` row is reconciled to `error` + "interrupted by
  restart".
- "null means not known": nullable columns arrive as JSON `null` (they are pointers on the wire). In
  lists they are omitted; on detail pages they render as "Not stated". A null
  `career_site_url_verdict` is a real state of its own — no validation has judged the current URL —
  and renders as "Not validated" rather than "Not stated".
- A run's counters (`items_found`/`items_inserted`/…) stay 0 while a job runs and jump to the final
  tally only when it finishes; live progress comes from the log/trace, not those counters.
- `docs/ui-design.md` is the **v1 design record** for the jobs list/detail viewer and the company
  directory. Its screens are built; its "API ↔ UI boundaries" and "null means not known" rules are the
  live guidance the current screens follow.
