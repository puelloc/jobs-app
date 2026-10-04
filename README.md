# jobs-app

Finds remote software-engineering jobs, and the companies that might have them, in one SQLite
database: **`jobs.db`** in this directory (gitignored — no data is ever committed).

Four workstreams write to it:

| Workstream | Reads | Writes |
| --- | --- | --- |
| **Job scrape** | a job board (RemoteOK today) | `job_listings`, plus a `companies` row per employer |
| **Company sources** | an index or listing page (the Wikipedia S&P 500/400/600 pages today) | `companies`, with `company_sources` recording which source contributed each one |
| **Careers resolution + validation** | a company's homepage | `companies.career_site_url`, `url_resolution_attempts`, and a browser verdict on the result |
| **Careers → listings pipeline** | the resolved careers sites (browser-use) | `company_application_platforms` (vendor classification) and `job_listings` (remote-US postings) |

**Sources are data, not columns.** `platforms` is the source registry and `company_sources` records
which sources contributed which company, so adding a source — another index, an exchange listing, an
import — needs a `platforms` row and a `company_sources` row, and no schema change. The S&P lists are
one contributor among several, not a special case.

## Where to start

| Document | What it covers |
| --- | --- |
| `docs/scraper-design.md` | the RemoteOK scraper's run behavior (v1) |
| `docs/remoteok-mapping.md` | RemoteOK → `job_listings` field mapping |
| `docs/sp1500-plan.md` | the company/careers workstream: its plan, its decisions and its measured results |
| `docs/scraping-plan.md` | the listings workstream: its plan and handoff (implemented — see its status note) |
| `docs/browser-use-worker.md` | the Go ↔ Python browser-use worker contract |
| `internal/db/MIGRATIONS.md` | the migration conventions and the one-off script rule |
| `web/README.md` | the web dashboard: trigger/monitor/pause/resume jobs; runs, companies, jobs, logs |

Run reports live in `docs/runs/`, one file per real-network run, with the numbers read back from the
database rather than from a command's summary line.

## Current state of `jobs.db`

| | Count |
| --- | ---: |
| Companies | 1,588 |
| `company_sources` rows | 1,589 (wikipedia_sp500 500, sp400 399, sp600 599, remoteok 91) |
| `job_listings` | 99 (RemoteOK, 2026-09-22) |
| Stored `career_site_url` | 834 |
| — browser-validated | 644 (503 confirmed, 64 wrong, 77 unverifiable) |
| — not yet validated | 190 |

Build and test from the repo root. The Go caches are in-repo because the default `$HOME` cache
locations are not writable here:

```
export GOCACHE=$PWD/.gocache GOPATH=$PWD/.gopath
go build ./... && go test ./...
```

## Deploy to the NAS (Docker)

Two images: the Go API + browser-use worker (`Dockerfile`), and the SvelteKit UI (`web/Dockerfile`).
`server` and the UI are long-running services; the one-off jobs run against the same `./data` volume.

```
./scripts/deploy.sh          # git pull + rebuild + rolling restart + verify
```

That script handles the `PUID`/`PGID` ownership of `./data` (SQLite must be writable by the
container user), rebuilds with `docker compose up -d --build --remove-orphans`, and reports any
restart-loop or API failure. The API is on host port `8094`, the UI on `8095`.

A fresh `./data` volume starts empty. Populate it from the UI's "Trigger a job" buttons (on the Runs
dashboard) in this order, or with the equivalent `docker compose run` commands:

```
docker compose run --rm app sp1500                                # 1. index the S&P 500/400/600 companies
docker compose run --rm app sp1500 resolve                        # 2. resolve each company's careers URL
docker compose run --rm app sp1500 validate                       # 3. browser-validate the careers URLs (optional)
docker compose run --rm app classify -commit                      # 4. classify each company's ATS vendor
docker compose run --rm app batch                                 # 5. full browser-use scrape sweep
```

`sp1500` (indices) writes the companies; `sp1500 resolve` finds their careers URLs (classify cannot
run without it); `sp1500 validate` marks each URL confirmed/wrong/unverifiable in a browser.
`classify -commit` records each company's applicant-tracking vendor (the scrape trigger refuses
unclassified companies). `batch` then runs the browser-use listings scrape sequentially — one company
at a time, so SQLite sees one writer and the model host sees one browser agent. `scrape -slug <slug>
-vendor <vendor>` runs a single company instead, and `scraper` refreshes the separate RemoteOK job
source. The `./data` volume carries `jobs.db` and the per-run agent traces across recreations.

The full sweep is driven from the UI ("Full scrape sweep" on the Runs dashboard), not the CLI. It
takes options the CLI also accepts via `batch -skip-ok -skip-traced -from-slug <slug>
-stop-after-failures N`:

- **Skip companies whose last run succeeded** (`-skip-ok`) — re-run only companies whose latest
  listings run did not finish `ok`.
- **Skip companies with a browser-use trace** (`-skip-traced`) — re-run only companies whose latest
  run left *no* trace events. A trace is the reliable "browser-use actually ran" signal: a run can
  report `ok` even when the agent died before its first step, so this is the way to target exactly
  the silent failures.
- **Start after slug** (`-from-slug`) — resume a sweep from a point.
- **Stop after N failures** (`-stop-after-failures`) — halt a sweep once a run of consecutive
  failures happens, leaving a resume point in the log.

Each company scrape pre-flights the model host before starting the browser agent: if Ollama cannot
be reached after **3 retries**, that company fails fast (exit code 6) with the reason recorded, and
the sweep **stops** rather than churning through every remaining company. Before attempting each
company the sweep writes its slug as a resume point (`<DATA_DIR>/sweep/resume`), so a stopped sweep
can be continued with one click — the Runs dashboard's sweep panel shows a **"Resume from `<slug>`"**
button whenever a resume point exists, and clears it when a sweep runs to completion.

Each listings run records the company it scraped (`scrape_runs.company_id`, migration 016), which is
what lets the skip decision know "did this company already succeed / already leave a trace". Each
job posting records the run that scraped it too (`job_listings.scrape_run_id`, migration 017), so the
job page can show the run's agent trace under "Why this job matched" — the reasoning that admitted a
posting that turns out not to be a software-engineering role.

The browser-use agent uses the `qwen38-q3-64k:latest` Ollama model by default (a q3 quantization for
lower VRAM and faster inference). Override it per deploy with the `BROWSER_USE_MODEL` environment
variable on the `app` service.

When a listings scrape finishes successfully, it closes that company's postings on that vendor which
the board no longer advertises (`status='closed'`), and a posting that reappears is reopened — so a
dropped listing stops looking live. Both paths share one implementation (`store.MarkJobsStale`); the
listings path narrows it by company, because many companies share one vendor platform. Two guards
keep a partial observation from closing live postings: an empty observed set closes nothing, and a
run that hit the scrape's `-max-jobs` cap is treated as partial and skips the close. `GET /api/jobs`
therefore defaults to `?status=open`; pass `?status=all` to see every status.

The UI talks to the API over the compose network (it proxies `/api/*` to `app:8080`); it does not
need the API host port exposed, which is kept for direct `curl` use.

## Known issues

- **Mojibake in scraped text.** RemoteOK's payload carries UTF-8 bytes that read as Latin-1 for common
  punctuation, so descriptions, positions, and some locations are stored with artefacts such as `Iâm`
  where the source intends `I’m`. This affects 78 of 99 descriptions in the 2026-09-22 capture
  (`data/raw/remoteok-2026-09-22.json`). Text is stored exactly as received and is **not** repaired:
  cleanup is a separate pass, and repairing at write time would make the stored value disagree with the
  raw capture kept beside it.
- **Lost evidence for some resolution attempts.** 1,867 `url_resolution_attempts` rows have
  `evidence_path` values under `/tmp/sp1500-live/`, a scratch directory that no longer exists; the
  attempt records survive but their captured bodies do not. Query it with
  `SELECT count(*) FROM url_resolution_attempts WHERE evidence_path LIKE '/tmp/%'`. See
  `docs/runs/2026-09-27-browser-validation.md`.
