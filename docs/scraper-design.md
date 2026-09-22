# Scraper design: v1 (RemoteOK)

## Purpose
Fetch the RemoteOK job feed once, persist the untouched payload, and turn it into normalized rows in `job_listings`. It is a single-source, single-run batch command that exits when the run finishes; it is not a daemon, not a scheduler, and not an application bot. It writes only to the existing schema - no schema changes are proposed here.

## Run lifecycle
Steps run in this order, in one process invocation. Steps 12-13 do not run if the run is aborted earlier.

1. **Load config.** Read environment variables, apply defaults, validate. No I/O yet. A bad value exits here (exit 2) with no trace left in the database.
2. **Open DB.** Open `DB_PATH` through the migration plumbing that already exists. This applies the DSN pragmas (`journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`, `synchronous(NORMAL)`) and runs any pending migrations. Writes: pending migrations, `schema_migrations` rows. On a normal run this step is a no-op.
3. **Insert `scrape_runs` row.** One row with `platform_id = 1` (the `remoteok` seed id), `started_at = now` (RFC3339 UTC), `status = 'running'`, all counters `0`, `finished_at` NULL, `error_text` NULL. This row's `id` is the run id used in logs and in every later step. This row is committed immediately, before the network is touched, so a crashed run is visible as a stuck `running` row instead of a silent gap.
4. **Fetch.** One HTTP GET of the configured endpoint with the configured `User-Agent` and timeout. Nothing is written to the database during the fetch. In memory only: HTTP status, response body bytes, redirect chain.
5. **Persist raw payload to disk.** Write the response bytes verbatim to the raw file (naming in Durability). This happens **before any parsing**, so a parse bug can never cost us the data. Written: the raw file only - no DB write yet.
6. **Parse.** Parse the bytes as JSON into a generic structure. No normalization decisions here.
7. **Extract identifiers, then normalize.** First walk every job element and pull its per-job identifier into the run's **seen set**. Only then turn each element into a candidate `job_listings` row: company identity, title, URLs, timestamps, salary integers, JSON-encoded blobs, `discovery_platform_id = 1`. Elements that cannot be normalized are collected as per-row problems and skipped, not fatal - and crucially, an element that yields an identifier but then fails normalization **keeps its place in the seen set**. Observed and upserted are separate sets, and only the seen set decides closure.
8. **Upsert jobs.** In one transaction, insert-or-update every normalized row. Counters: `items_inserted`, `items_updated`. Written: `companies` rows (created as needed), `job_listings` rows.
9. **Mark stale jobs.** In the same transaction, set `status = 'closed'` for this source's rows whose `external_id` is **not in this run's seen set** (Freshness contract). Timestamps play no part in this decision. Skipped entirely if the seen set is empty **or** if zero rows were accepted (a run that wrote nothing has no business closing anything). Written: `status` on `job_listings` rows only.
10. **Commit.** The upserts and the stale-marking land together or not at all.
11. **Finish `scrape_runs` row.** Update the step-3 row: `finished_at`, `status = 'ok'`, and the final counters. Written: one `scrape_runs` row.
12. **Print result line to stdout.**
13. **Exit 0.**

Step 5 is deliberately before step 6. If parsing panics or the normalizer is wrong, the previous step already put the exact bytes on disk, so the fix is a re-parse, not a re-fetch.

## Inputs
All are read from the environment. No flags, no config file, no prompts.

| Name | Default | Required/optional | Purpose |
| --- | --- | --- | --- |
| `DB_PATH` | `./jobs.db` | optional (required in practice on the NAS) | SQLite file to open and migrate. |
| `DATA_DIR` | `./data` | optional | Parent directory for raw payloads; the file lands in `$DATA_DIR/raw/`, created if missing. |
| `REMOTEOK_ENDPOINT` | the RemoteOK URL already recorded in `platforms.base_url_pattern` for seed id 1 | optional | Full URL to GET. Exists so a changed endpoint is a config edit, not a rebuild. |
| `USER_AGENT` | `jobs-app/0.1 (+https://github.com/local/jobs-app)` | optional | Sent as the `User-Agent` header. Overridable so an operator can identify their own instance. |
| `HTTP_TIMEOUT` | `30s` | optional | Whole-request timeout, parsed as a Go duration. |
| `LOG_LEVEL` | `info` | optional | `info` prints the result line; `debug` additionally prints the run id and step timings. Never affects what is written to the DB. |

Resolution order is: explicit environment value, else default. An empty string is treated as unset.

## Outputs
A successful run touches exactly three things: `job_listings` rows, one `scrape_runs` row, and one raw file.

### job_listings rows
- **Contains:** one row per accepted job element from this run, for both new and re-observed jobs.
- **Written:** step 8 (upsert) and step 9 (stale status), committed together in step 10.
- **Human inspection after success:** rows for this source have `discovery_platform_id = 1`; `first_seen_at` is the UTC timestamp of the run that first saw each job; `last_seen_at` is the current run's time for every row observed in this run; `status` is `'open'` for observed jobs and `'closed'` for rows that were previously seen but absent this time; `raw_data` holds the verbatim per-job JSON; `created_at`/`updated_at` bracket the row's life. Rows are not deleted - a job that vanishes from the source stays in the table as `'closed'`.

### scrape_runs row
- **Contains:** exactly one new row per run, always, including failed runs that got past step 3.
- **Written:** step 3 (`running`) and step 11 (final). Aborted runs keep `status = 'running'` and a NULL `finished_at` - that is the marker of a crash or kill.
- **Human inspection after success:** one row with `status = 'ok'`, `started_at` < `finished_at`, `error_text` NULL, and `items_found` >= `items_inserted + items_updated` (the gap is duplicates within the payload plus rows rejected during normalization).

### raw payload file
- **Contains:** the response body exactly as received, byte for byte, with no pretty-printing, re-encoding, or trimming.
- **Written:** step 5, before parsing. Written even when the body later turns out to be unparseable.
- **Human inspection after success:** a dated JSON file under `$DATA_DIR/raw/`, openable with `jq` or a text editor. Note that its top-level length is **not** always `items_found`: the file holds every raw element, including any legal notice, while `items_found` excludes a detected notice. So `jq 'length'` on the file equals `items_found + 1` when a notice was detected and `items_found` when it was not, and there is no column that distinguishes those two cases. Counting the file is the reliable way to reconcile a run; comparing it directly to `items_found` is not.

## Failure policy
Retries exist in exactly one row (429). Every other network condition fails the run on first occurrence - no backoff, no jitter, no attempt cap to tune.

| Condition | Detection | Behavior | Exit code | scrape_runs.status |
| --- | --- | --- | --- | --- |
| DNS resolution failure | HTTP client returns a resolution error before any response | Fail immediately, log to stderr, do not write a raw file. Finish the run row with the error text. | 1 | `error` |
| TLS failure | Client returns a certificate/handshake error | Fail immediately, log to stderr, no raw file. Finish the run row. Never disable verification as a fallback. | 1 | `error` |
| HTTP 403 | Response status 403 | Fail immediately, log to stderr, discard the body without writing it. Finish the run row. | 1 | `error` |
| HTTP 404 | Response status 404 | Fail immediately, log to stderr, discard the body. Finish the run row. Treated as a config/endpoint problem, so the stderr line names the endpoint. | 1 | `error` |
| HTTP 429, first occurrence | Response status 429, and `Retry-After` is absent or a plain integer delta-seconds of at most 60 | Sleep once for `min(Retry-After, 60s)`, or 30s when the header is absent or unparseable; then retry the request once. Log that one retry is in flight. | - (run continues) | `running` |
| HTTP 429 with an oversized `Retry-After` | Response status 429 and `Retry-After` parses to more than 60s | Do **not** retry and do **not** sleep for the requested interval - a multi-hour sleep would hold the `running` row open and look like a hang. Treat it as permanent: fail, log to stderr, discard the body, finish the run row. | 1 | `error` |
| HTTP 429, second occurrence | Second response in the same run is 429 | Do not retry again. Fail, log to stderr, discard the body, finish the run row. | 1 | `error` |
| HTTP 5xx | Response status 500-599 | Fail immediately, log to stderr, discard the body, finish the run row. No retry in v1. | 1 | `error` |
| Request timeout | Context deadline exceeded after `HTTP_TIMEOUT` | Fail immediately, log to stderr, no raw file. Finish the run row. A partial body is never written. | 1 | `error` |
| Response is not valid JSON | JSON parse returns an error after the body was read | The raw bytes are already on disk, so keep the file and log its path. Fail, finish the run row. No jobs are upserted and no jobs are marked closed. | 1 | `error` |
| Response is a JSON array with zero job elements | Valid JSON, seen set is empty, raw element count == 0 | Keep the raw file (an empty body is evidence). Do **not** mark any job closed - an empty or broken feed must not close the whole board. Finish the run row with the error text. | 4 | `error` |
| Response is a JSON array with only the legal-notice element | Valid JSON, raw element count > 0 but seen set is empty, because no element carries a usable per-job identifier | Identical to the previous row: keep the raw file, skip stale-marking, finish as an error. | 4 | `error` |
| Identifiers observed but every element failed to normalize | Valid JSON, seen set is non-empty, accepted job count == 0 | Skip stale-marking: the seen set is non-empty, but a run that could not write a single row must not close rows either. Emit one stderr line per skipped element naming the reason, finish the run row as an error. Never exit 0: this is a mapping or normalization defect, not a quiet day. | 1 | `error` |
| DB is locked | A write returns `SQLITE_BUSY` after the 5s busy timeout, on the insert, upsert, or finish step | Retry the single failing statement once after 1s; if it still fails, fail the run and try to record it. The `scrape_runs` row already exists, so the run is visible even if the finish update is what failed. The raw file is already on disk. Log the DB path and the failing step to stderr. | 3 | `error` if the finish update lands, else `running` |
| Migration fails on startup | `Open` returns a migration error naming a file | Fail before any `scrape_runs` row exists - the process exits without touching the network. Log the migration filename to stderr. | 2 | no row written |

Exit codes: `0` success, `1` network/protocol/parse/normalization failure, `2` config/migration/startup failure, `3` database write contention, `4` valid response with no usable job elements, `5` run succeeded but the raw capture for this date already existed (see Durability).

## Idempotency contract
**Invariant:** running the scraper twice in a row creates zero duplicate job rows and is safe to do by accident. Two runs over the same payload produce the same set of rows as one run.

- **Upsert key:** the partial unique index `unique_job_listings_by_discovery` on `(discovery_platform_id, external_id) WHERE discovery_platform_id IS NOT NULL AND external_id IS NOT NULL`, taken with `discovery_platform_id = 1` and the source's own per-job identifier as `external_id`. `company_application_platform_id` stays NULL for this source, so the other partial unique index is not involved.
- **Precondition:** a job element must yield a non-empty, stable per-job identifier. If it cannot, the row is skipped and counted as a normalization problem rather than inserted with a NULL `external_id`, because NULL would silently disable the dedupe index and produce duplicates on the next run. Such an element is also absent from the seen set, so the two sets are built from the same predicate and can never disagree about identity. An element that *has* an identifier but fails later normalization is in the seen set and not in the upsert set - which is exactly the case closure must not treat as absence.
- **On conflict, updated:** `last_seen_at` (to this run's time), `updated_at`, `status` (to `'open'` - see Freshness), `raw_data` (replaced with the current payload for that job), and the content fields as re-parsed this run: `title`, `listing_url`, `application_url`, `discovery_url`, `external_id`, `employment_type`, `is_remote`, `location_text`, `country`, `is_us`, `description`, `salary_min_cents`, `salary_max_cents`, `salary_currency`, `salary_period`, `tags_json`, `posted_at`. Re-observation is authoritative: if the source changed a field, the stored value changes with it.
- **On conflict, preserved:** `id`, `first_seen_at`, and `created_at` are never touched by an upsert. `first_seen_at` means "the first run that ever saw this job" and is immutable for the row's life, including across a close/reopen cycle.
- **On conflict, conditional:** `company_id` is re-resolved each run; if the normalized company string changes for the same job, the row is repointed to the matching `companies` row rather than being duplicated. `company_application_platform_id` is written as NULL for this source.
- **Stale-marking is idempotent:** step 9 sets `status = 'closed'` on rows outside the run's seen set. Running it twice matches the same rows and writes the same value. Because the decision is set membership rather than a clock comparison, the result does not depend on timing, slow runs, or clock accuracy.
- **Counters:** a re-observation increments `items_updated`, never `items_inserted`. A row that is closed and then re-observed counts as an update.

## Freshness contract
- **Cutoff:** a job from this source is marked `status = 'closed'` when its `external_id` is **not in the current run's seen set** - the set of per-job identifiers extracted from the payload at step 7, before normalization. There is no time-based cutoff. `last_seen_at` records when a job was last observed and is never used to decide closure, precisely because it would silently serve as a proxy for "observed" and would be wrong for any element that was seen but failed normalization.
- **Ordering:** stale-marking runs after the upserts in the same transaction, so rows written by this run are already reopened and cannot be closed by it. The ordering is a convenience rather than a correctness requirement: the two sets are disjoint regardless of order.
- **Reappearance:** if a closed job appears in a later run, the upsert flips `status` back to `'open'` and refreshes `last_seen_at` and `updated_at`. `first_seen_at` is preserved, so the row keeps its original discovery date; `created_at` is untouched.
- **Scope:** only rows with `discovery_platform_id = 1`. Rows from other sources are never closed by this scraper.
- **No deletions:** absence closes a row; it never removes it.
- **Rows this rule cannot close:** a shell row whose upsert key is missing, or a row whose `external_id` is NULL, is outside step 9's comparison and is left with whatever status it already had. This is deliberately conservative: a row we cannot identify is also a row we cannot confidently declare absent. It means a job that the source permanently drops *and* that also loses its identifier will not be closed automatically.
- **Two guards:** step 9 is skipped entirely when the seen set is empty, and also when zero rows were accepted for upsert. An empty feed cannot close the whole board, and neither can a run that observed identifiers but failed to write any of them.
- **Failure mode this protects against, part one:** the source silently dropping listings from its feed, which would otherwise leave those jobs looking permanently `'open'` with a stale `last_seen_at`. The close/reopen cycle makes the source's own publication window explicit in the data instead of implying that every job ever seen is still live.
- **Failure mode this protects against, part two:** a single mapping regression - a renamed field, an unanticipated null - causing elements that are present in the payload to be absent from the upsert set. Under a timestamp-based rule those elements would look gone and be closed en masse. Deriving closure from the pre-normalization seen set means a normalization failure degrades into "this row was not updated", never "this row was closed".

## Durability
- **Filename pattern** (the run's `started_at` converted to UTC, `DATA_DIR` default `./data`):

```
$DATA_DIR/raw/remoteok-YYYY-MM-DD.json
```

- **Overwrite policy:** the file is created with `O_CREATE|O_EXCL`, so the first run of a UTC day keeps its payload and a second run that day **fails the raw-write step** rather than overwriting. An existing file is never truncated, appended to, or replaced. There is deliberately no `-2`, `-001`, or timestamp-of-day suffix: the file answers "what did the source serve on this date", and two answers for one date would be ambiguous.
- **Atomicity:** the payload is written to a temp file in the same directory and renamed into place, so a crash mid-write cannot leave a truncated file that looks like a valid capture.
- **Same-day collision behavior:** process the run normally - the data in hand is valid, so the DB writes happen exactly as on any other run - but exit `5` and log the existing path to stderr. Exit `5` is **not** a failure and **not** a config error: the success line still prints, carrying `raw=existing`, so exit-code watchers see a completed run and humans see that the on-disk capture for today is an earlier one. Exit `2` is reserved for configuration and migration problems, and reporting a clean run as `2` would make a healthy rerun indistinguishable from a broken start.
- **Why the dump exists:** so the mapping can be re-derived from bytes already on disk. When normalization logic changes, the fix is re-parsing a stored payload, not re-fetching a live feed. That keeps re-processing free of the source's availability, rate limits, and drift, and makes a mapping change testable against a fixed input. It is also the only record of what the source actually served, which is what makes any later disagreement with the mapping doc checkable.

## Observability
### stdout on success
Exactly one line, written at step 12 when the run ends with exit `0` or exit `5`. Space-separated `key=value` pairs, fixed order, no timestamps inside the line (the logging layer adds those), no job content:

```
run=<scrape_runs.id> source=remoteok status=ok http=200 bytes=<n> path=<raw file> raw=<written|existing> found=<n> inserted=<n> updated=<n> closed=<n> duration_ms=<n>
```

`closed` is the number of rows moved to `status='closed'` by step 9. `path` is the absolute path of the raw file. `raw=written` means this run created that file; `raw=existing` means the file was already there and this run's payload was **not** persisted (exit `5`) - the counters still describe this run's DB writes.

### stdout/stderr on failure
- **stdout:** nothing. The success line is not printed on exits `1`, `2`, `3`, or `4`, so its presence means "this run did its job" - exit `5` still prints it, because the run did succeed.
- **stderr:** exactly one line per run, in the same `key=value` shape but with the failing step and the condition, as `run=<id|none> source=remoteok status=error step=<config|open|fetch|raw|parse|normalize|upsert|stale|finish> condition=<short label> http=<code|-> err=<message> path=<raw file|-> exit=<n>`. The `step` value names where it died; `http` and `path` are `-` when the failure happened before a response or before the raw file was written.
- The `err` value is the underlying library error string, single-line (newlines escaped), truncated to 1000 characters.
- A skipped row during normalization gets its own stderr line at `debug` level only. The one exception is the "identifiers observed but nothing normalized" case in the Failure policy table, where the per-element reasons are promoted to `info` so the cause is visible without raising log level. Row-level problems never abort the run and never multiply into one line per field.
- Exit codes are as listed in the Failure policy table, and exit `5` is a success variant rather than a failure.

### scrape_runs fields

| Column | What this scraper writes |
| --- | --- |
| `id` | Left to SQLite; the value returned by the step-3 insert is the run id used in every log line. |
| `platform_id` | `1` - the `remoteok` seed id, constant for this scraper. Never derived from the payload. |
| `started_at` | RFC3339 UTC, captured immediately before the step-3 insert. Also the source of the raw filename date. Written once, never updated. It is deliberately **not** used as a freshness cutoff - closure is decided by the seen set. |
| `finished_at` | RFC3339 UTC, written at step 11 (success), in the failure path once the run is known dead, and on exit `5`. NULL for the whole run while `status='running'`; stays NULL if the process is killed. |
| `status` | `'running'` at step 3; `'ok'` at step 11 for exits `0` and `5`; `'error'` on any handled failure after step 3. Never `'ok'` for a run that accepted zero jobs. |
| `items_found` | Count of raw elements in the payload, excluding the legal-notice element if one was detected. Counts what the source offered, whether or not it normalized, so it is an upper bound on the seen set rather than a count of it. Note the raw file on disk still contains the notice, so its length is this value or this value + 1. |
| `items_inserted` | Count of `job_listings` rows newly created in step 8. Zero on a repeat run of the same payload. |
| `items_updated` | Count of existing `job_listings` rows updated in step 8, including rows flipped from `closed` back to `open`. |
| `error_text` | NULL on success. On failure, a single line: the condition label plus the truncated error message, matching the `condition=`/`err=` values on stderr. Contains no response body and no job content. |

### Not logged
- **Full job descriptions.** Descriptions are large, multi-line, and may carry HTML; they inflate every log line and can inject newlines or control characters into log-based tooling. Only presence and length are ever logged, at `debug`. The text itself lives in `job_listings.description` and `raw_data`.
- **PII.** Any contact name, email, phone number, or applicant-facing detail that appears in the payload stays in the database columns and the raw file. It is never copied into stdout, stderr, or `scrape_runs.error_text`. Logs are the artifact most likely to be shipped off-box, so they carry the least sensitive content.
- **Response bodies.** Neither success nor error paths echo body bytes; the raw file is the single place the payload lives. A non-JSON response is especially not echoed - it may be an HTML error page or a captive-portal redirect, and it is useless in a log line. Where the body matters, the log points at the raw file path instead.
- Logging is limited to counts, durations, status codes, the run id, the endpoint host (not the full URL with any query string, at `info`), and file paths.

## Non-goals for v1
- No pagination. One request, one response, one run.
- No concurrency: one goroutine, one request at a time, one DB connection, jobs processed sequentially.
- No retries beyond a single bounded 429 retry. No exponential backoff, no jitter, no circuit breaker.
- No in-process scheduler, cron, systemd unit, or container config - how often this runs is an operator decision outside the binary.
- No cross-source deduplication or merge. The same real-world job seen on two sources stays two rows.
- No US / remote classification logic. `is_us`, `country`, and `location_text` are populated only from what the payload states; no inference from text.
- No HTML cleaning, markdown stripping, or description normalization. Stored as received.
- No application automation, form filling, or credential handling of any kind.
- No rate limiting beyond a single `User-Agent` and a request timeout. No request budgeting across runs.
- No notification, alerting, metrics endpoint, or web UI.
- No cleanup pass for abandoned `scrape_runs` rows left in `status='running'` by a crash or a kill, or by the exit-`3` path where the finishing write itself failed. Those rows are visible and diagnosable, and reclaiming them is an operator or later-step concern.
- No schema changes, no new tables, no new indexes.

## Open questions requiring a live sample
- Is the response a bare JSON array, and is the first element always a legal notice, and is that notice detectable by a stable property rather than by position?
- What is the stable, source-unique identifier per job, and is its type consistent across rows (always a string, always numeric, or mixed)?
- Are identifiers stable across runs for the same job, or can they change when the listing is edited?
- Are salary values dollars or cents, annual or hourly, and what value represents "not specified" - absent, null, empty string, or zero?
- Is a salary currency present anywhere in the payload, and if so, is it on every job or only some?
- Is there a field that states the pay period, or must it be inferred from magnitude?
- What is the published timestamp field, what format and timezone is it in, and does it agree with any epoch-seconds field that is also present?
- Do the listing URL and the application URL ever differ, and can either be empty or missing?
- What does the location value actually contain - a city, a region, a country, a free-text phrase, or a remote marker?
- Is there any field that reliably indicates a job is closed or expired, or must closure be inferred purely from absence in a later run?
- Are tags always an array, can they be null, can they be an empty array, and what element types appear in them?
- Is the description always present, is it always HTML, and are there rows where it looks like plain text?
- How many distinct company strings appear, and which ones collide when compared case-insensitively or after stripping suffixes?
- Is there any field indicating seniority, department, or employment type that maps to `employment_type` without guessing?
- Does the payload ever contain non-job elements other than the legal notice, and if so, what property distinguishes them?
- What is the observed response size and element count, to set sensible defaults for `HTTP_TIMEOUT` and any future body-size limit?

## Schema concerns
- `job_listings.external_id` is nullable, and the partial unique index `unique_job_listings_by_discovery` does not cover rows where it is NULL. This scraper therefore cannot persist a job element that carries no usable identifier without either duplicating it on every run or creating a row that closure can never reach. The design resolves this by skipping such elements entirely, which keeps the schema's partial index honest, but it means a job with no stable identifier is invisible to the app even though it was present in the payload. Whether the schema should allow identifier-less rows at all is a separate decision, and no change is proposed here.
- `scrape_runs` has no column distinguishing the total raw element count from `items_found`. `items_found` excludes a detected legal notice, so a stored capture's top-level length is either equal to it or one greater, with nothing recording which. Reconciling a raw file against a run therefore requires re-parsing the file. Recording both counts would require a schema change, so this is noted rather than fixed.
