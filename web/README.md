# jobs-web

Read-only SvelteKit dashboard over the Go API. Three screens:

- **Runs** at `/` — the status and history of every `scrape_runs` row: running runs first, then
  finished ones, with a count of active runs. It is the only screen that polls (every 5s), because a
  run is in flight and its status is the thing changing.
- **Companies** at `/companies` and `/companies/[id]` — a directory of companies and, per company,
  the careers-site resolution trail behind it. The directory shows each company's `career_site_url`
  (linked), its validation verdict (`confirmed`/`wrong`/`unverifiable`), and its attempt count, with
  filters for `resolution`, `index`, and `search`. The detail page shows the company's facts and every
  `url_resolution_attempts` row — accepted and rejected alike.
- **Jobs** at `/jobs` and `/jobs/[id]` — the scraped job listings, with a per-job page showing the
  description (rendered as escaped plain text, never as HTML) and the listing/application/discovery
  URLs.

There is no write path, no auth, and no client-side fetching on first load: every page is
server-rendered, and only then does the browser fetch more (the runs poll, and the companies/jobs
"load more" buttons that request the next offset).

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
- `src/lib/api.js` is the only module that fetches. It exports `getRuns`, `getCompanies`,
  `getCompany`, `getJobs`, and `getJob`.
- The first paint is server-rendered: each `+page.js` calls its `get*` function during SSR, so the
  initial HTML already contains the data. `+page.svelte` then keeps it live where it matters:
  `/` re-runs its load on a 5-second interval via `invalidate('data:runs')`, so a run in flight moves
  from "running" to its result without a manual refresh; `/jobs` and `/companies` append further
  pages with the "load more" button (their data changes slowly, so they do not poll).
- During SSR the request is addressed to the dev server's own origin (`url.origin`) instead of a bare
  relative path, because SvelteKit's own `event.fetch` resolves same-origin requests through this
  app's router, which has no `/api` route. Addressing the origin keeps one code path for both the
  server render and the browser.

## The API the screens use

| Endpoint | Used by the UI |
| --- | --- |
| `GET /api/runs` | **yes** — the runs dashboard (`/`) |
| `GET /api/companies` | **yes** — the company directory (`/companies`) |
| `GET /api/companies/{id}` | **yes** — a company + its resolution trail (`/companies/{id}`) |
| `GET /api/companies/churn` | no |
| `GET /api/jobs` | **yes** — the jobs list (`/jobs`) |
| `GET /api/jobs/{id}` | **yes** — a job's detail (`/jobs/{id}`) |

`/api/companies/churn` exists for the data model (accepted careers URLs that moved between runs) and
has no screen yet; nothing in this app calls it.

## Layout

```
src/
  app.html
  lib/
    api.js            fetch wrapper; throws { status, code, message }; exports getRuns, getCompanies, getCompany, getJobs, getJob
    format.js         pure formatting helpers (formatUtc, formatRunStatus, formatDuration, formatVerdict, formatJobStatus, formatValidationStatus, formatSalary, formatLocation, humanize, NOT_STATED, …)
  routes/
    +layout.svelte    the shell: top bar, brand, nav (Runs / Companies / Jobs)
    +page.svelte      runs dashboard
    +page.js          server-side load of GET /api/runs?limit=100&offset=0, plus the poll
    companies/
      +page.svelte    company directory
      +page.js        load of GET /api/companies (filters ride the URL query string)
      [id]/
        +page.svelte  one company: facts plus the whole resolution trail
        +page.js      load of GET /api/companies/{id}
    jobs/
      +page.svelte    jobs list
      +page.js        load of GET /api/jobs?limit=25&offset=0
      [id]/
        +page.svelte  one job: facts, dates, links, description
        +page.js      load of GET /api/jobs/{id}
    +error.svelte     error route
```

## Notes

- Svelte 5 runes mode is forced on in `svelte.config.js` (`compilerOptions.runes: true`), so legacy
  syntax is a compile error.
- A run's `status` is the whole point of the screen: `running` means the row was written before the
  network was touched and has not been finished yet, so a run stuck in `running` is a crash or a kill
  rather than a quiet success.
- "null means not known": nullable columns arrive as JSON `null` (they are pointers on the wire). In
  lists they are omitted; on detail pages they render as "Not stated". A null
  `career_site_url_verdict` is a real state of its own — no validation has judged the current URL —
  and renders as "Not validated" rather than "Not stated".
- `docs/ui-design.md` is the **v1 design record** for the jobs list/detail viewer and the company
  directory. Its screens are now built; its "API ↔ UI boundaries" and "null means not known" rules
  are the live guidance the current screens follow.
