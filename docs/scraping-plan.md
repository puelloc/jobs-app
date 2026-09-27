# Scraping listings from the resolved careers sites — plan and handoff

> **Status: implemented.** This workstream is now built and runs end to end. The pipeline is:
> `sp1500` (index companies) → `sp1500 resolve` (careers URLs) → `sp1500 validate` (browser-check,
> optional) → `classify -commit` (ATS vendor) → `batch` (listings sweep, one company at a time). It
> is driven from the web UI's "Trigger a job" buttons and monitored via the runs dashboard, the run
> detail page, and the Logs page. See `README.md` (Deploy) and `web/README.md` for the current shape;
> this document is the plan it was built from.

The careers-site workstream (resolve a company to a careers URL, then validate that URL) is finished.
This document is the starting point for the next one: **fetch actual job listings from those careers
sites into `job_listings`**.

| Field | Value |
| --- | --- |
| Current state | Implemented: a browser-use agent finds the filtered listings URL, a render pass extracts postings, and `cmd/scrape`/`cmd/batch` upsert the US-only ones |
| Next action | Deploy to the NAS and run the sweep (classify first) |
| Blocking issues | none |
| **Canonical database** | **`jobs.db` in the repo root** — one file, no per-source copies |

---

## 1. The input: where the careers sites are

**`jobs.db`** — the repo-root database that already existed, migrated to the current schema and
consolidated on 2026-09-27. It is gitignored (`*.db`), like all data in this project.

It now holds both workstreams:

| | Count |
| --- | ---: |
| Companies | 1,588 |
| **Stored `career_site_url`** | **834** |
| Of those, validated | 644 |
| - `confirmed` | 503 |
| - `wrong` | 64 |
| - `unverifiable` | 77 |
| Stored but **not yet validated** | 190 |
| `job_listings` (RemoteOK) | 99 |
| `company_sources` rows | 1,589 |

### How it was consolidated, and why there were two files

There should never have been two. The root `jobs.db` was created by `config.DefaultDBPath = "./jobs.db"`,
which is relative to the working directory, so the first scraper that ran from the repo root created it.
The S&P 1500 work was then given an explicit `DB_PATH=/tmp/sp1500-live/jobs.db`, which is how a second,
source-named database appeared — and `/tmp` was wiped soon after, taking that file with it.
Frozen pre-merge copies are kept as a safety net under `data/backups/` (§8).

The merge moved the S&P rows into the original file and is recorded in
`internal/db/oneoff/consolidate_sp1500_into_jobs_db.sql` (a one-off script, deliberately **not** a migration: it moves rows
between files and must never run on a fresh database). Both files had independent id sequences, so
`companies.id` was rejoined by slug and `scrape_runs.id` was offset by 1000; `url_resolution_attempts`
was remapped through both. One company — `johnson-controls` — existed in both and now carries two
`company_sources` rows (`remoteok` and `wikipedia_sp500`).

### Sources are data now, not columns

Migration `013_company_sources.sql` adds `company_sources(company_id, platform_id, source_key,
first_seen_at, last_seen_at)`. That is the generic place for "which sources contributed this company":
the S&P 500 is just `platform_id = 20`, one contributor among many, alongside `remoteok = 1`.

```
wikipedia_sp600  599      remoteok  91
wikipedia_sp500  500
wikipedia_sp400  399
```

`companies.index_membership` (`sp500`/`sp400`/`sp600`) and its neighbours (`ticker`, `cik`,
`gics_sub_industry`, `headquarters_location`) are one source's attributes and are now legacy: they are
still written by the S&P bootstrap, but a new company source needs a `platforms` row and a
`company_sources` row, and no schema change at all.

The scraping input, in the order worth using it:

```sql
-- 1. The clean set: a careers page that loaded and was judged a job listing or a careers landing page.
SELECT id, slug, name, career_site_url
  FROM companies
 WHERE career_site_url_verdict = 'confirmed'
 ORDER BY slug;

-- 2. The 190 that have a URL but no verdict yet. Unvalidated, not rejected - a resolver found them
--    after the validation snapshot.
SELECT id, slug, name, career_site_url
  FROM companies
 WHERE career_site_url IS NOT NULL AND trim(career_site_url) <> ''
   AND career_site_url_verdict IS NULL
 ORDER BY slug;

-- 3. Explicitly not input: the 64 wrong and the 77 unverifiable. Do not scrape these until they are
--    re-judged; #6 explains how.
SELECT id, slug, career_site_url, career_site_url_verdict, career_site_url_verdict_url
  FROM companies
 WHERE career_site_url_verdict IN ('wrong','unverifiable')
 ORDER BY slug;
```

**Read the verdict, not just the URL.** `career_site_url_verdict_url` is the URL the verdict was
reached about; the verdict is only trustworthy while it still equals `career_site_url`. A later resolve
run that repoints a company leaves the two disagreeing, and the company should be treated as
unvalidated (that is exactly what the validation skip rule does).

Other useful columns on `companies`: `website`, `career_site_url_source`, `career_site_url_checked_at`,
`career_site_url_http_status`, `career_site_url_title`, `career_site_url_verdict`,
`career_site_url_verdict_at`, `career_site_url_verdict_run_id`, `career_site_url_verdict_url`.

## 2. What already exists that the scraper should reuse

The RemoteOK scraper is the working exemplar of the shape this project wants; read it before writing a
new one.

| Piece | Where | What to reuse |
| --- | --- | --- |
| Pipeline conventions | `docs/scraper-design.md` | Raw bytes to disk **before** parsing; a `scrape_runs` row first; one stdout `key=value` line; exit codes 0/1/2/3/4/5; idempotent upsert; closure from a pre-normalization *seen* set |
| Raw capture | `internal/rawstore` | Dated, `O_EXCL`, temp-file-and-rename. Shared by every command already |
| Run lifecycle | `store.StartRun` / `store.FinishRun`, `store.StartDryRun` | The `scrape_runs` row is committed before the network is touched |
| Upsert + dedupe keys | `job_listings` indexes | `unique_job_listings_by_discovery(discovery_platform_id, external_id)` and `unique_job_listings_by_application_system(company_application_platform_id, external_id)` |
| An exemplar scraper | `internal/scraper/remoteok` | fetch → capture → parse → normalize → upsert → close stale |
| ATS identity | `careers.ParseTenant` (Greenhouse/Lever/Ashby) | Tenant + vendor from a URL |
| Rendered fetch | `internal/browseruse` + `worker/browser_worker.py` | A real browser when a plain fetch is refused; contract in `docs/browser-use-worker.md` |
| Per-host politeness | `internal/httpfetch` limiter | Per-host concurrency 1, global cap, 500 ms floor, retries only on 5xx/429. **The new crawl should use this; the validation pass did not have a per-host limiter and got away with it at concurrency 2** |

`job_listings` columns: `company_id`, `company_application_platform_id`, `external_id`,
`discovery_platform_id`, `discovery_url`, `listing_url`, `application_url`, `title`, `employment_type`,
`is_remote`, `location_text`, `country`, `is_us`, `description`, `salary_*`, `tags_json`, `posted_at`,
`first_seen_at`, `last_seen_at`, `status`, `raw_data`, `created_at`, `updated_at`. It holds the 99
RemoteOK listings today, so the careers-site scraper is adding rows to a non-empty table — the
`discovery_platform_id` / `company_application_platform_id` split is what keeps the two sources apart.

`platforms` bands (from `002`'s header, keep them): job boards **1–9** (1–4 are remoteok, remotive,
himalayas, weworkremotely), application systems **10–19** (greenhouse, lever, workday, icims, ashby,
smartrecruiters, custom) and **30–34** (oraclecloud, eightfold, phenom, successfactors, taleo),
reference sources **20–29** (wikipedia ×3, career_resolution, career_validation).

Two decisions this workstream has to make that the schema cannot:

1. **Which platform row a listing discovered on a company's own careers site belongs to.** The next
   free job-board-band id is **5**; something like `career_site` / `job_board` is the natural seed, and
   it is the `discovery_platform_id` while `company_application_platform_id` stays NULL.
2. **What to write when the listing is really on an ATS tenant.** Then `company_application_platform_id`
   is the vendor's id (10–16, 30–34) and the *concrete tenant URL* goes in
   `company_application_platforms.base_url` — that table is **empty right now**, so ATS identity has to
   be derived at scrape time from the URL/host.

## 3. What the validation pass learned that the scraper will hit

Measured on the 644 URLs validated so far, in `docs/runs/2026-09-27-browser-validation.md`:

- **64 sites (10.1%) answer HTTP 403 to a real browser.** They are not wrong URLs; they refuse
  automated clients. A scraper will meet the same wall. `internal/browseruse` exists for this, but see
  the cost note below.
- **Some `confirmed` URLs are landing pages, not listings.** The gate accepts the company's own careers
  entry point when it is careers-shaped and names the company; those pages often link out to an ATS
  board. The scraper must follow one hop to the board rather than parse the landing page for jobs.
- **`company_application_platforms` is empty**, so nothing has recorded which vendor hosts which
  company. Expect to derive it per company (the plan's tier 5a/5b fingerprints) and to populate that
  table as a side effect.
- **The browser-use agent costs ~6 minutes per site** and did not beat a hard bot wall on the one site
  sampled. It is an investigation tool, never a bulk fetch path.
- **Rendered evidence is already on disk** for the validation run:
  `data/sp1500-live/data/raw/careers/<slug>/<run>/<index>-<host>.html` — 512 KB prefixes for accepted
  pages, full bodies for rejected ones. Parser work can start against those pages offline instead of
  re-fetching 500 sites.

## 4. Constraints to preserve

These are the conventions the rest of the tree is built on, and the tests assume them:

- **Raw before parse.** A parse bug must be a re-parse, not a re-fetch.
- **Run row first**, finish it `ok`/`error`; an aborted run stays `running` as the crash marker.
- **Idempotency.** Two runs over the same payload produce the same rows; an upsert updates
  `last_seen_at` and content, preserves `first_seen_at`.
- **Closure by seen set**, not by clock: a job absent from the payload is closed, and a run that
  accepted nothing closes nothing.
- **NULL beats fabricated.** A wrong URL or a guessed salary poisons the pipeline; skip and record.
- **TDD, no live network in tests.** Recorded fixtures, temp-file SQLite, table-driven, behavioural test
  names.

## 5. Suggested first slice

**Corrected against measurement.** The obvious first slice - scrape the confirmed URLs hosted on
Greenhouse, Lever or Ashby, because those have verified public JSON APIs - does not exist in this
data:

```sql
SELECT count(*) FROM companies
 WHERE career_site_url_verdict = 'confirmed'
   AND (career_site_url LIKE '%greenhouse.io%' OR career_site_url LIKE '%lever.co%'
     OR career_site_url LIKE '%ashbyhq.com%');
-- 0
```

**Zero.** The resolution ladder preferred a company's branded page over a third-party board (that is
its recorded preference), so `career_site_url` is a first-party page for essentially the whole
confirmed set: 495 first-party hosts, 1 Workday, 0 recognised ATS hosts. Of the 496, 458 did not
redirect at all and the 38 that did redirected to another first-party URL. **The ATS tenants are
therefore not in `career_site_url`; they are behind the pages it points at.**

### Where the boards actually are

The validation pass retained a 512 KB prefix of every accepted page, so the vendor map can be mined
from disk instead of guessed. Scanning the 784 retained HTML files for job-board host patterns:

| Vendor | Companies whose retained page references it | Access |
| --- | ---: | --- |
| Workday | 141 | HTML only |
| Oracle Cloud | 35 | HTML only |
| SuccessFactors | 29 | HTML only |
| iCIMS | 20 | HTML only |
| Phenom | 17 | HTML only |
| Eightfold | 12 | HTML only |
| Taleo | 10 | HTML only |
| Greenhouse | 6 | public JSON API |
| Lever | 3 | public JSON API |
| SmartRecruiters | 3 | no reliable API (plan §4.13) |
| Ashby | 2 | public JSON API |

**264 of the references are HTML-only vendors, against 11 for the JSON APIs.** That confirms the
plan's own note that HTML extraction (M2b) matters more than the ATS API layer (M2a) - and it means
the scraping workstream's centre of gravity is HTML parsing, not REST.

Caveat: this is a scan of *references* in retained pages - nav and footer links included - not a
verified tenant list. Treat it as a lead list, and expect to confirm each host per company as the
scraper touches it. `company_application_platforms` is empty, so nothing has recorded a tenant yet;
populating it as the scraper resolves each company is a natural side effect.

### A first slice that exists

1. **The 11 hand-verified companies in plan §4.11** (JPMorgan Chase, Nike, Coca-Cola, Salesforce,
   Costco, ExxonMobil, Pfizer, Boeing, Verizon, Goldman Sachs, Lockheed Martin). Their expected careers
   URLs, titles and ATS hosts are already recorded there, and four are vendor-hosted. Use them to get
   the *pipeline* right - raw capture, normalize, upsert, close stale, idempotency - against known
   expectations rather than against a vendor's markup.
2. **Then one JSON-API vendor end to end** (Greenhouse, Lever or Ashby; ~11 companies between them),
   because the fetch and the entry count are already solved in `internal/careers/ats.go`.
3. **Then one HTML vendor** (Workday first - 141 companies, the largest slice), which is where the
   real work is.

Whatever the slice, the shape is the RemoteOK one: raw capture, parse, normalize into `job_listings`,
upsert, close stale, one `scrape_runs` row, one summary line - and a second run asserting zero
duplicates.

## 6. Running the validation tool again (so it is not lost)

```bash
cd /Users/cris/Projects/jobs/jobs-app
export DB_PATH="$PWD/jobs.db" DATA_DIR="$PWD/data/sp1500-live/data"
export ENABLE_BROWSER_USE=1 BROWSER_USE_CONFIG_DIR="$PWD/.browseruse"
export BROWSER_WORKER_COMMAND="$PWD/.venv-browser/bin/python $PWD/worker/browser_worker.py"
go build -o data/sp1500-live/sp1500 ./cmd/sp1500

./data/sp1500-live/sp1500 validate                 # skips everything already validated
./data/sp1500-live/sp1500 validate --refresh       # re-validates all 834
./data/sp1500-live/sp1500 validate --stale-after 720h
./data/sp1500-live/sp1500 validate --escalate --only-slugs=<slug> --agent-timeout 6m   # needs OLLAMA_HOST
```

- **Skipping validated sites is the default** — a plain run reports `skipped=644` and picks up only the
  190 unvalidated URLs; the 77 `unverifiable` are left alone until they are stale or explicitly
  refreshed. **A default run therefore starts real network work**: use `--dry-run --limit 1` to see the
  selection without fetching anything.
- Python worker environment: `.venv-browser/` (gitignored), pinned in `worker/requirements.txt`.
  Rebuild with `/opt/homebrew/bin/python3.12 -m venv .venv-browser && ./.venv-browser/bin/pip install -r worker/requirements.txt`.
- `docs/browser-use-worker.md` is the worker contract; `internal/careers/validate.go` holds the gate,
  including the **validation profile** (`RequireJobListingEvidence`) that the discovery path does not use.

## 7. Open questions

1. **Is a company careers site a discovery source or an enrichment of an existing listing?** It decides
   whether a listing found there is a new `job_listings` row (`discovery_platform_id` = 5) or a match
   against one already found on a job board. Nothing in the schema forces the answer.
2. **Which ATS vendors get HTML extraction first?** Oracle Cloud, Eightfold, Workday, Phenom,
   SuccessFactors and Taleo have no public JSON API; the plan's M2b is that work.
3. **Do we scrape the 190 unvalidated URLs, and re-validate the 77 unverifiable first?** The clean 503
   are the safe start.
4. **What is the per-host rate policy for a 500-site crawl?** The resolution path has a limiter; the
   new job should reuse it rather than invent a second policy.

## 8. Repository state

**All committed; the working tree is clean as of 2026-09-27.** The careers-site workstream was closed
out and the browser-validation tier, the RemoteOK normalizer, and the store reads were committed as
separate units (`2c0671c` onward), so `git status` is empty and `go build ./... && go test ./...`
passes. There are no in-flight files to avoid sweeping up, and no uncommitted work to lose.

Two notes for a future session:

- `career-check/` (a one-off browser profile with ~1,000 vendored extension files) is now gitignored
  rather than committed. The scratch copy this workstream used at `/tmp/sp1500-live/` has been
  deleted: its URLs were merged into the durable database, verified byte-identical or additive, and
  its raw Wikipedia payloads were copied into `data/sp1500-live/data/raw/`.
- The Go toolchain caches are in-repo (`.gocache/`, `.gopath/`), so build and test need
  `export GOCACHE=$PWD/.gocache GOPATH=$PWD/.gopath GOFLAGS=-mod=mod` — see `.gitignore` for why.

Artifacts from the validation pass:

- `docs/runs/2026-09-27-browser-validation.md` — the measured outcome and the four defects fixed.
- `docs/runs/2026-09-27-residue-after-join-fix.md` — the tier-1 re-keying fix and its measurement.
- `jobs.db` — the merged database described in §1.
- `data/sp1500-live/data/raw/` — the stored Wikipedia payloads and per-company careers evidence.
- `data/sp1500-live/*.out` / `*.err` — raw run logs, including the per-company progress lines.
