# RemoteOK → job_listings mapping

## Source facts
- Raw file: **not found** — `data/raw/remoteok-YYYY-MM-DD.json` does not exist; there is no `data/` directory and no `*.json` file anywhere in the repo (checked the full working tree, `git ls-files`, all git history, and stashes).
- Total elements: unknown — no source file to count.
- Legal notice present at index 0: unknown.
- Job count after skipping notice: unknown.
- Fields observed across all jobs (union of keys, sorted): unknown.

Status: **blocked — raw dump missing or empty.** Per instruction, no mapping was produced and nothing was fetched from RemoteOK.

Inputs that were available and read in full:
- `internal/db/migrations/001_init.sql` — present.
- `internal/db/migrations/002_seed_platforms.sql` — present; `remoteok` is seed id `1`, `platform_type = 'job_board'`, matching the `discovery_platform_id` role.

Every remaining section (column mapping, dropped fields, ambiguous fields, company name handling, schema concerns) requires field observations from the raw dump, so none of it can be written without inventing facts.
