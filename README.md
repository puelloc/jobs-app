# jobs-app

Finds remote software-engineering jobs, and the companies that might have them, in one SQLite
database: **`jobs.db`** in this directory (gitignored — no data is ever committed).

Three independent workstreams write to it:

| Workstream | Reads | Writes |
| --- | --- | --- |
| **Job scrape** | a job board (RemoteOK today) | `job_listings`, plus a `companies` row per employer |
| **Company sources** | an index or listing page (the Wikipedia S&P 500/400/600 pages today) | `companies`, with `company_sources` recording which source contributed each one |
| **Careers resolution + validation** | a company's homepage | `companies.career_site_url`, `url_resolution_attempts`, and a browser verdict on the result |

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
| **`docs/scraping-plan.md`** | **the next workstream**: fetching listings from the resolved careers sites |
| `docs/browser-use-worker.md` | the Go ↔ Python browser-use worker contract |
| `internal/db/MIGRATIONS.md` | the migration conventions and the one-off script rule |
| `web/README.md` | the runs dashboard |

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
