# Job viewer — v1 design

## Purpose

A read-only web view of the scraped software-engineering job listings in `jobs.db`: a list of jobs, most recent first, and a page for one job. It is built for one person running it on their own machine or LAN, with no accounts and no server to operate beyond the two processes. It replaces typing `sqlite3`/`psql` queries and eyeballing raw rows to answer "what did the scraper find, and is it still live?".

## Non-goals

- No applying to a job.
- No viewing application status.
- No triggering the scraper from the UI.
- No saving, favoriting, hiding, or annotating jobs.
- No search, full-text, tag filtering, or saved filters.
- No charts, dashboards, stats, or "insights".
- No notifications or emails.
- No authentication of any kind.

## Data the UI reads

Semantics come from `docs/scraper-design.md`. `status` is not a clock: a row is `'closed'` because the most recent successful run for its source did not see its `external_id` in the feed; reappearance flips it back to `'open'`. `first_seen_at` is the run that first ever saw the job and is immutable for the row's life, including across a close/reopen cycle. `last_seen_at` is when the job was last observed, refreshed on every observation, and is never used to decide closure. The UI must present those three as distinct facts, not synonyms.

| Schema column | Source table | Shown where | Notes |
| --- | --- | --- | --- |
| `id` | `job_listings` | Detail route path only | Integer rowid alias; builds `/jobs/{id}` and `GET /api/jobs/{id}`. Never rendered as text. |
| `title` | `job_listings` | List title, Detail heading | NOT NULL. Plain text. |
| `company_id` | `job_listings` | Not rendered; join key | NOT NULL FK to `companies(id)`. Never emitted as a number; used only to resolve `companies.name`. |
| `name` | `companies` | List subtitle, Detail subheading | Resolved through `company_id`. NOT NULL, and `foreign_keys(ON)` means the join should always resolve; if a row is missing anyway, render "Unknown company" rather than blank. |
| `status` | `job_listings` | List row badge, Detail header | NOT NULL, one of `open`/`closed`/`filled`/`unknown`. Text always, color on top; never color alone. |
| `employment_type` | `job_listings` | List subtitle, Detail facts | Nullable. Null → the segment is omitted, not shown as "unknown". |
| `location_text` | `job_listings` | List subtitle, Detail facts | Nullable free text. Null → fall through to `country`, then to the remote flag. |
| `country` | `job_listings` | Detail facts | Nullable ISO-3166 alpha-2. Null → omitted. |
| `is_remote` | `job_listings` | List subtitle, Detail facts | NOT NULL 0/1, emitted by the API as a boolean. `true` → "Remote"; `false` → omitted. |
| `is_us` | `job_listings` | Detail facts | Nullable 0/1, emitted as `true`/`false`/`null`. Only `true` renders a "US" tag; `false` and `null` both render nothing in v1. |
| `salary_min_cents` | `job_listings` | List metadata, Detail compensation | Nullable minor units. |
| `salary_max_cents` | `job_listings` | List metadata, Detail compensation | Nullable minor units. |
| `salary_currency` | `job_listings` | List metadata, Detail compensation | Nullable ISO-4217. Null → amounts render with no currency symbol. |
| `salary_period` | `job_listings` | List metadata, Detail compensation | Nullable `year`/`month`/`hour`. Null → the "/ period" suffix is omitted. |
| `posted_at` | `job_listings` | List metadata, Detail facts | Nullable; as published by the source, so it may be absent or disagree with our first sighting. |
| `first_seen_at` | `job_listings` | List metadata, Detail facts | NOT NULL. "First seen by this app". |
| `last_seen_at` | `job_listings` | List metadata, Detail facts | NOT NULL. "Last seen by this app". |
| `listing_url` | `job_listings` | Detail link | NOT NULL. The URL that was scraped. |
| `application_url` | `job_listings` | Detail link | Nullable. Hidden when null. |
| `discovery_url` | `job_listings` | Detail link | Nullable. Hidden when null. |
| `description` | `job_listings` | Detail body only | Nullable, may be large and may carry HTML. Never sent in the list response. |

## API contract

Read-only. GET only; no POST, PUT, PATCH, or DELETE exists in v1. JSON in, JSON out, no cookies, no auth headers. Two endpoints — a third (`GET /api/sources`) is deliberately omitted because v1 displays one source and offers no source filter, so it would have no consumer.

| Method | Path | Query params | Response shape | Used by |
| --- | --- | --- | --- | --- |
| GET | `/api/jobs` | `limit` (default 25, max 100), `offset` (default 0) | List envelope: a `jobs` array of list items plus `limit`, `offset`, `total` | List route |
| GET | `/api/jobs/{id}` | none | One job object, the list item plus description and the three URLs | Detail route |

**List envelope** (`total` is the count of matching rows and is not a job field; the UI may use it only to decide whether to offer "load more"):

```json
{
  "jobs": [
    {
      "id": "<integer>",
      "title": "<string>",
      "company_name": "<string>",
      "status": "<open|closed|filled|unknown>",
      "employment_type": "<full_time|part_time|contract|intern|temporary|unknown, or null>",
      "location_text": "<string or null>",
      "country": "<ISO-3166 alpha-2, or null>",
      "is_remote": "<boolean>",
      "salary": {
        "min_cents": "<integer or null>",
        "max_cents": "<integer or null>",
        "currency": "<ISO-4217, or null>",
        "period": "<year|month|hour, or null>"
      },
      "posted_at": "<RFC3339 UTC string, or null>",
      "first_seen_at": "<RFC3339 UTC string>",
      "last_seen_at": "<RFC3339 UTC string>"
    }
  ],
  "limit": "<integer>",
  "offset": "<integer>",
  "total": "<integer>"
}
```

**Detail object** — the same fields as a list item, plus `description` (string or null, verbatim as stored), `listing_url` (string), `application_url` (string or null), and `discovery_url` (string or null). The API does not include `raw_data`, `external_id`, `company_id`, `company_application_platform_id`, `discovery_platform_id`, `tags_json`, `created_at`, or `updated_at`.

**Pagination:** offset/limit. It is the simplest scheme that a single-user SQLite-backed read view needs, and nothing in v1 writes rows while a page is being read, so there is no skipped-or-duplicated-row problem for a cursor to solve. `limit` is clamped to 1–100 and `offset` to ≥ 0; values outside that range are a 400 rather than silently corrected.

**Sorting:** `COALESCE(posted_at, first_seen_at) DESC, id DESC`. `posted_at` is the most literal reading of "most recent first" but is nullable, so `first_seen_at` (never null) is the fallback and `id` breaks ties deterministically. Not user-changeable in v1.

**Errors:** every non-2xx response is `{"error": {"code": "<string>", "message": "<string>"}}`, with a short machine code such as `bad_request`, `not_found`, or `internal`. The UI must handle 400 (invalid or out-of-range query params), 404 (no job with that id, or an unknown path), and 500 (anything unexpected). Error messages are for the operator's eyes, not for parsing.

## Routes and screens

| Route | Renders | Fetches from | Notes |
| --- | --- | --- | --- |
| `/` | List view | `GET /api/jobs?limit=25&offset=0` | Server-rendered. "Load more" requests the next offset client-side. |
| `/jobs/{id}` | Detail view | `GET /api/jobs/{id}` | Server-rendered. A 404 from the API renders the not-found route. |
| not-found route | "Page not found" / "Job not found" | nothing | Catches unknown paths and unknown job ids. |

Server-rendered for both real routes: the content is in the first HTML response, which keeps a read-only viewer working without client-side fetching and matches how little the pages change. A job id that once existed but is now gone still resolves to a row (rows are never deleted), so the 404 path is for genuinely unknown ids.

## List view — what it shows

Per row, in order:

- **Title** — `title`, plain text, and the link into `/jobs/{id}`.
- **Subtitle line** — `companies.name` via `company_id`, joined with ` · ` to `employment_type` and the location, each omitted when its column is null. Company renders as "Unknown company" only if the join finds no row.
- **Location** — `location_text`; when that is null, `country`; when both are null and `is_remote` is true, "Remote"; when all three yield nothing, the segment is dropped.
- **Status** — `status` as text plus color: `open` green, `closed` grey, `filled` and `unknown` grey. The text always carries the meaning, so color is redundant.
- **Salary** — from `salary_min_cents`/`salary_max_cents`/`salary_currency`/`salary_period`, e.g. "$120,000–$160,000 / yr". Both amount columns null → the segment is blank, not "$0" or "—". One side null → "from X" or "up to X". `salary_currency` null → amounts render with no symbol; `salary_period` null → no "/ yr" suffix.
- **Posted** — `posted_at` as a date; null → the label is omitted entirely.
- **First seen / Last seen** — `first_seen_at` and `last_seen_at` as dates under those exact labels, so they are not mistaken for the posting date. `first_seen_at` is not repeated as "posted".

`description` is never shown in the list at any length: it is large, multi-line, possibly HTML, and belongs on the detail page only.

- **Empty state** — when `total` is 0: "No jobs yet. Run the scraper, then reload this page." Nothing in the UI can start the scraper (see Deferred); the text is the whole remedy.
- **Loading state** — a single-line "Loading jobs…" placeholder; no skeleton.
- **Error state** — one line naming the failure plus a "Try again" link that re-requests the same URL; retrying is a read, not a mutation.

## Detail view — what it shows

In order:

- **Title** — `title` as the heading.
- **Company** — `companies.name` via `company_id`; "Unknown company" if the join finds no row.
- **Status line** — `status` text plus color, elaborated with `last_seen_at`: "Open — last seen 3 days ago" or "Closed — last seen 12 days ago". A closed job is closed because the latest run did not see it, so the "last seen" age is the only honest freshness signal available.
- **Facts** — `employment_type`, location (`location_text`, else `country`, else the `is_remote` flag), a "US" tag only when `is_us` is true, and the compensation line built from the four salary columns exactly as in the list (all null → "Not stated").
- **Dates, separately labeled** — "Posted by the source" (`posted_at`, null → "Not stated"), "First seen by this app" (`first_seen_at`), "Last seen by this app" (`last_seen_at`). Three distinct labels, never a single generic "date".
- **Links** — `listing_url` always rendered as "View listing" (`listing_url` is NOT NULL); `application_url` rendered as "Apply on the company site" and hidden when null; `discovery_url` rendered as "Original source page" and hidden when null.
- **Description** — rendered as escaped plain text in a pre-wrapped block. Descriptions are third-party payload stored as received and may contain HTML, so escaping is the safe default and v1 ships no sanitizer; sanitized HTML is a deliberate later decision, not a gap to fill here. Null → "No description provided."
- **Back to list** — a plain link to `/`.

Not shown, and deliberately absent from the API: `raw_data` (verbatim payload, large, and may contain the PII the scraper doc keeps out of logs), `external_id` and the numeric foreign keys (`company_id`, `company_application_platform_id`, `discovery_platform_id`), `created_at`/`updated_at` (bookkeeping, not user-facing), `tags_json` (unparsed JSON with no v1 display decision), and `id` outside the URL.

## API ↔ UI boundaries

The API owns the data contract: filtering (none in v1, but the place a filter would go), pagination, sorting, and turning SQLite's representation into stable JSON — notably `is_remote`/`is_us` from 0/1, nullable columns into JSON `null`, and dropping columns the UI must not receive (`raw_data`, `description` in the list). It emits no HTML, no formatted money strings, no relative dates, and no human labels; it returns raw column values plus the envelope `total`.

The UI owns presentation: date and money formatting, relative-time phrasing ("last seen 12 days ago"), labels and headings, color, and omitting a field when its value is null. It must not compensate for an inconsistent API: if a required field is missing or a nested shape changes, that is an API bug, not something the UI normalizes around; and the UI must never reconstruct a value the API withheld. The one shared rule is that null means "not known" and is omitted or shown as "Not stated", never as an empty string or a fabricated default.

## Deferred — v2 and beyond

- **Applying to a job** — needs a write endpoint and a place to store submissions, which does not exist in the schema; the UI seam is a single action on the detail page.
- **Application status** — needs the same store plus a read endpoint and a status surface on the detail page or a new route.
- **Triggering the scraper from the UI** — needs a non-GET endpoint that invokes the existing CLI and returns a run id; the UI seam is a control on the list page's empty state.
- **Saving, favoriting, hiding, annotating** — needs the project's first schema change (a column or table for the flag) plus write and filter paths; the UI seam is a per-row control and a list filter.
- **Search, full-text, tag filtering, saved filters** — needs a query parameter on `/api/jobs` backed by `LIKE` or an FTS index, and `tags_json` parsing if tags are the filter; the UI seam is a filter bar above the list.
- **Charts, dashboards, stats, insights** — needs an aggregate endpoint over `job_listings` and/or `scrape_runs`; the UI seam is one additional read-only route.
- **Notifications and emails** — needs a delivery mechanism entirely outside the API and UI (a scheduler or worker reacting to status changes); the UI seam is at most a preferences surface.
- **Authentication** — needs middleware in front of every Go handler and session handling in SvelteKit; the API seam is one wrapper around the two handlers, the UI seam is a sign-in route.

## Open questions

- Does the list need a `source` column in v1, or is remoteok the only source the UI will ever show?
- Should closed jobs be visible in the default list, or hidden behind a toggle — and if hidden, does `/api/jobs` gain a `status` parameter?
- Is offset/limit acceptable, or does the current row count already warrant a cursor?
- When `posted_at` and `first_seen_at` disagree, which one does "most recent first" mean to the reader, and should rows with a null `posted_at` sink or interleave?
- How large is a real `description` in practice, and should the detail view cap its height or always render it in full?
- Is `description` sometimes plain text rather than HTML, and if so, would sanitized HTML rendering be worth the complexity in v1?
- Are `filled` and `unknown` ever produced by an actual run, and if not, should the UI stop styling them?
- Should the detail view link to `companies.career_site_url`, which exists in the schema but is unused by this design?
- Is a rowid-alias `id` stable enough to bookmark and share, or should detail URLs key on something the source controls?
- Should the UI distinguish `is_us = 0` from `is_us = NULL`, or is treating both as "no US tag" fine?
- Should `tags_json` be displayed on the detail page, and if so, how should a malformed or empty array be handled?
- Should the empty or error state show when the scraper last ran, which would need a third endpoint reading `scrape_runs`?
- Is a 100-row maximum page size ever reached, or is the default 25 always enough?
