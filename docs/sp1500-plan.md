# S&P 1500 company bootstrap → careers-site resolution — implementation plan

## Status

| Field | Value |
| --- | --- |
| Current milestone | **M1 — ready to implement, unblocked** |
| Next action | Commit 1: migration-runner FK-off support + its 5 tests (see "Commit order") |
| Blocking issues | none for M1 or M2 |
| Open questions | 3, all non-blocking (see "Unresolved") |
| Last corrected | row counts `503 / 399 / 600 = **1,502**` |

This document is the single source of truth for the S&P 1500 workstream. It is self-contained:
it embeds the measurements, the decisions, and the traps, so a new session can continue without
re-deriving any of them.

---

## 1. Goal

Build a recurring (weekly) system that finds **companies with remote software-engineering jobs**
and resolves each company's **careers site URL**, the entry point for scraping actual listings.

The immediate work is narrower than the goal, and the split is deliberate:

- **S&P 1500 bootstrap** — scrape three Wikipedia index pages into `companies` (identity only).
- **Careers resolution** — turn identity into a working `career_site_url`.

**These pages give company identity, not hiring signal.** The S&P 600 in particular skews
small-cap financials, industrials, REITs and utilities. Discovery (which companies actually hire
remote engineers) is **out of scope for M1/M2** and belongs against the seeded remote job boards
(`platforms` ids 1–4: remoteok, remotive, himalayas, weworkremotely).

## 2. Constraints

- **Go.** Project is Go 1.27.1 with `modernc.org/sqlite` (pure Go, no cgo) — chosen so a NAS build
  cross-compiles to linux/amd64, linux/arm64, darwin/arm64 from one machine with no C toolchain.
- **One SQLite database:** `jobs.db`.
- **Strict TDD.** ~2,900 lines of existing tests: golden fixtures, real temp-file SQLite fixtures,
  table-driven tests, behavioral test names, **no live network in tests**.
- **Conventions to preserve:**
  - Raw payload bytes are written to disk **before** parsing (a parse bug is a re-parse, not a re-fetch).
  - Every run inserts a `scrape_runs` row first, then finishes it `ok`/`error`.
  - One stdout `key=value` success line.
  - Exit codes: `0` ok, `1` network/parse, `2` config/migration, `3` DB contention, `4` valid-but-empty,
    `5` same-day raw collision.

## 3. Milestones

| Milestone | Scope | Status |
| --- | --- | --- |
| **M1** | S&P wikitext parser + golden fixtures + `companies` enrichment columns + `platforms` `'reference'` rebuild + migration-runner FK-off support + `scrape_runs`/exit codes | **ready** |
| **M2a** | Careers ladder tiers 1–4 + `5a` (verified public ATS JSON APIs) + `5c` (SmartRecruiters) | planned |
| **M2b** | `5b` HTML-only ATS tenant extraction (Oracle Cloud, Eightfold, Workday, Phenom, SuccessFactors, Taleo) | planned |
| **M3** | browser-use behind `ENABLE_BROWSER_USE`, Go→Python subprocess, shared validation gate | deferred |

HTML extraction (M2b) matters more than the ATS API layer (M2a), because the most common
enterprise ATSs are HTML-only.

---

## 4. Verified source facts

Everything in this section was established by running a probe, not by assumption. Where a claim
was later falsified, the correction is recorded in §9.

### 4.1 The three pages have no website column

| Page | Columns |
| --- | --- |
| S&P 500 | `Symbol \| Security \| GICS Sector \| GICS Sub-Industry \| Headquarters Location \| Date added \| CIK \| Founded` |
| S&P 400 | `Symbol \| Security \| GICS Sector \| GICS Sub-Industry \| Headquarters Location \| SEC filings` |
| S&P 600 | `Symbol \| Security \| GICS Sector \| GICS Sub-Industry \| Headquarters Location \| SEC filings \| CIK` |

The only URL-ish cell is the SEC filings link (`sec.gov/cgi-bin/browse-edgar?CIK=...`) — EDGAR,
not the company. **Websites and careers URLs must come from an external source.**

Fetch raw wikitext:

```
curl -s -H "User-Agent: <configurable>" \
  "https://en.wikipedia.org/w/index.php?title=List_of_S%26P_500_companies&action=raw" -o sp500.wiki
```

### 4.2 Row counts — the primary test assertion

| Page | Data rows | Security cell has wikilink | Plain text | CIK source |
| --- | --- | --- | --- | --- |
| S&P 500 | **503** | 500 | 3 | explicit CIK column, 10-digit zero-padded |
| S&P 400 | **399** | 356 | 43 | no CIK column; `CIK=` in SEC link (119 numeric, 281 ticker-like) |
| S&P 600 | **600** | 392 | 208 | explicit CIK column |
| **Total** | **1,502** | | **~253** | |

A **data row** is a row block containing a ticker template. That predicate is what yields these
numbers, and it is the predicate the parser must use.

`503 + 399 + 600 = 1502`. State this derivation in the test comment; it has already been
mis-added once.

Cross-checks: no duplicate tickers within any component table. The S&P 500 page's own prose says
the index "comprises 503 common stocks", corroborating the 503. The S&P 600 page's prose claims
603 stocks while the table has 600 data rows — the prose appears stale, so assert the table.

### 4.3 Cell and template syntax differ by page

- **S&P 500** uses `||`-prefixed cells, each on its own line: `|| {{NyseSymbol|MMM}}`.
- **S&P 400** uses styled cells: `| style="border-color:inherit;" | {{NyseSymbol|DAR}}`.
- **S&P 600** uses bare `|` cells with an anchor glued to the ticker: `|{{Anchor|A}}{{NyseSymbol|AAMI}}`.

**Ticker templates — six variants observed:**

```
{{NyseSymbol|X}}  {{NasdaqSymbol|X}}  {{BZX link|X}}  {{NYSE Arca|X}}  {{nyse|X}}  {{NASDAQ|X}}
```

`{{BZX link|CBOE}}` carries the 503rd S&P 500 row and must be handled.

### 4.4 The contamination trap

The component table is the **first** `{|` … matching `|}`. Ticker templates also appear in **prose
inside the change-log tables** further down each page, e.g. S&P 600 lines 5615–5830:

```
Xperi Holding Corporation was renamed to Adeia Incorporated ({{NASDAQ|ADEA}}).
...changed its name and ticker symbol to Stellar Bancorp ({{NASDAQ|STEL}}).
AAN was spun off from S&P 400 parent Aaron's Holding Company, Inc. ({{nyse|PRG}})...
```

A whole-page template scan counts these and inflates the result (this produced a bogus 601, and
separately a bogus 606). **Any extraction outside the component table is a bug**, and it fails by
returning a *plausible* wrong number rather than an error.

Nested tables: none in any of the three fixtures (depth-matched close == first close for all three),
but parse with depth counting anyway — see §6.4 (a).

### 4.5 Plain-text rows are companies with no Wikipedia article

The ~253 rows whose Security cell is plain text are **not** a formatting accident. Verified: "Agree
Realty", "AZZ, Inc.", "Chemed Corp.", and "Darling Ingredients" all return no enwiki article, and
enwiki search returns the S&P list pages themselves.

Consequence: **article title is only a ~83% join key.** An unresolved title is a legitimate outcome,
not a parser bug. These rows need a non-Wikipedia identity source (§5.4).

### 4.6 CIK availability differs per page

- **S&P 500:** explicit CIK column, 503 numeric values, 10-digit zero-padded (`0000066740`).
- **S&P 600:** explicit CIK column; row-scoped count is 600, matching the row count. A whole-page
  grep finds 603 — again prose contamination (§4.4). Assert 600.
- **S&P 400:** no CIK column. Extract `CIK=` from the SEC link. Of 399 values, **119 are numeric and
  281 are ticker-like** (`CIK=DAR`). Store raw; **do not coerce ticker-like values to integers.**
  Resolution via SEC `company_tickers.json` is a later step.

Note the S&P 500 yields 503 numeric CIKs but only 500 security-cell wikilinks. Do not couple those
counts — they legitimately differ (BRK.B, BF.B, and Insulet are the plain-text three).

### 4.7 Wikidata is the best bulk website source — but naive use is wrong

Matching by **enwiki article title**:

```
https://www.wikidata.org/w/api.php?action=wbgetentities&sites=enwiki
  &titles=<urlencoded title1|title2|...>&props=claims|sitelinks&sitefilter=enwiki&format=json
```

- Up to **50 titles per call**.
- The response `entities` object is keyed by **QID**, not title — recover the title from
  `entity['sitelinks']['enwiki']['title']`.
- **P856** = official website. **Ticker is not a direct value**: it is qualifier **P249** on a **P414**
  (stock exchange) statement. This is why the obvious SPARQL query returns zero results.
- **Redirect canonicalisation is mandatory.** 85 titles are redirects; without a canonicalisation
  pass, 83 titles falsely appear unresolved (e.g. `[[AMD|Advanced Micro Devices]]`).
- Resolution rate after canonicalisation: **1,241 / 1,246 = 99.60%**.

**Measured P856 distribution (1,237 matched entities):**

| P856 count | Entities |
| --- | --- |
| 0 | 16 |
| exactly 1 | 931 |
| 2–5 | 261 |
| >5 | 29 |

**Rank matters.** 180 entities have ≥1 `preferred`; 978 are normal-only; 108 have ≥1 `deprecated`.
Apple is the *outlier*: 109 values, 108 `normal` + 1 `deprecated`, zero `preferred`. Most major
companies (Microsoft, JPMorgan, Exxon, Coca-Cola, Nike, McDonald's, Intel, Walmart, Costco) have a
correct `preferred`.

**Do not filter on P407 (language) or P1001 (country).** Apple's main site carries a `P1001=US`
qualifier and Intel's is tagged "American English", so both misfire on cases you want to keep.

**Measured yield:**
- Candidate homepage: **1,206 / 1,237 entities (97.49%)** = 1,215 / 1,502 rows (80.9%).
- Live precision, random sample of 40: **38/40 ≈ 95%** (2 wrong: Visa, stale P856 redirecting to a
  subsection; Netflix, wrong-entity fork).
- Infobox cross-check on zero-P856 entities: **14/16 = 87.5%**, with 6/6 host agreement on controls.

**Heuristic (as measured):** drop `deprecated`; normalise URL; classify locale-specific if a path
segment matches `^[a-z]{2}(-[a-z]{2,4})?$` / `^[a-z]{2}_[A-Za-z]{2}$` (after file-extension strip) or
the host is a cc domain; prefer the non-locale pool (else tag `locale_only`); then lexicographic:
non-locale → `preferred` → most-common host → fewest path segments → no query → shortest path →
fewest subdomains → TLD rank (`.com` < `.org` < `.net` < `.gov` < `.edu`) → https → shorter URL.

### 4.8 Redirect targets can be a different company ⚠

Audited all 85 redirect links: **7 hard errors** (Knife River Corporation → MDU Resources; IAC Inc.
→ People Incorporated; Reynolds Consumer Products → Reynolds Group Holdings; Antero Midstream →
Antero Resources; Crane Merchandising Systems → Crane Payment Innovations; Bristow Group Inc. →
Bristow Helicopters; Adeia → Xperi) ≈ 8% of that subset, plus **7 systematic parent→subsidiary
swaps** for bank/utility holdcos (KeyCorp → KeyBank, Fifth Third Bancorp → Fifth Third Bank, …)
which silently yield the consumer site instead of the corporate site.

**Guard:** require the redirect target's label to share a token with the security name (or check P31).

### 4.9 Wikipedia infoboxes

```
https://en.wikipedia.org/w/api.php?action=parse&page=<Title>&prop=text&format=json&redirects=1
```

Clean canonical homepages (`Apple Inc.` → `https://apple.com`, `AAON` → `http://aaon.com`), no
locale pollution. But `missingtitle` for companies with no article — i.e. exactly the §4.5 rows. A
good tier-2 fill, **not a complete answer**.

### 4.10 SEC EDGAR

- `data.sec.gov/submissions/CIK*.json` has `website` and `investorWebsite` fields — **all empty**
  across every company tested. **Do not plan on these.**
- `sec.gov/files/company_tickers.json` (10,428 entries) maps ticker→CIK→name, **no website column**.
  Useful for resolving the 281 ticker-like S&P 400 CIKs.
- **The 10-K filing does contain the website.** Darling Ingredients → `https://www.darlingii.com`.
  Expensive (~4MB/filing) and must distinguish the corporate homepage from IR/newsroom/SEC-exhibit URLs.

### 4.11 The deterministic careers ladder works for most sites

Anchor-text + URL-pattern scan for `careers|jobs|join us|work with us|opportunities`, every result
verified by fetching and checking `<title>`:

| Company | Discovered | `<title>` |
| --- | --- | --- |
| JPMorgan Chase | `jpmorganchase.com/careers` | Careers \| JPMorganChase |
| Nike | `jobs.nike.com/` → `careers.nike.com/` | Nike Careers |
| Coca-Cola | `coca-colacompany.com/careers` | Careers at The Coca-Cola Company |
| Salesforce | `salesforce.com/company/careers/` | Salesforce Careers |
| Costco | `costco.com/jobs.html` → `/f/-/careers` | Costco Careers |
| ExxonMobil | `corporate.exxonmobil.com/careers` | Career opportunities \| ExxonMobil |
| Pfizer | `pfizer.com/about/careers` | Careers \| Pfizer |
| Boeing | `boeing.com/company/careers` | Careers |
| Verizon | `mycareer.verizon.com/` | Welcome to the V Team life |
| Goldman Sachs | `goldmansachs.com/careers` | Goldman Sachs Careers |
| Lockheed Martin | `lockheedmartin.com/en-us/careers/index.html` | Lockheed Martin Careers |

Secondary signals:
- `robots.txt` sometimes names the careers host (Verizon: `Sitemap: https://mycareer.verizon.com/sitemap.xml`).
- `sitemap.xml` sometimes lists careers URLs (Pfizer, Boeing).
- **ATS fingerprint falls out of the redirect for free** — Nike → Workday, Pfizer → Workday,
  JPMorgan → Oracle Cloud, Lockheed Martin → Eightfold.

**Decoys:** Lockheed's nav contains `href="#"` alongside the real link; Salesforce's nav offers
`trailhead.salesforce.com/career-path/` (a *product* page) competing with `/company/careers/`. A
selector that takes the first match gets both wrong.

### 4.12 ATS endpoints

**Verified public JSON APIs:**

| ATS | Endpoint | Observed |
| --- | --- | --- |
| Greenhouse | `boards-api.greenhouse.io/v1/boards/{slug}/jobs` | 200, 122KB, real `jobs[]`; entries contain careers host |
| Ashby | `api.ashbyhq.com/posting-api/job-board/{slug}` | 200, **13.8MB**, real `jobs[]` — stream/bound |
| Lever | `api.lever.co/v0/postings/{slug}?mode=json` | 200 valid; 404 `{"ok":false,"error":"Document not found"}` invalid |

**No public JSON API found** (do not plan on these): **Oracle Cloud, Eightfold, Phenom,
SuccessFactors, Taleo.** These are exactly where the verified redirect fingerprints point
(JPMorgan, Lockheed), so **tier 5b must scrape HTML**, not call an API.

### 4.13 HTTP 200 is not evidence ⚠

| URL | Status | Bytes | Title |
| --- | --- | --- | --- |
| `boards.greenhouse.io/zzzznotrealco999` | non-200 | — | — |
| `jobs.ashbyhq.com/zzzznotrealco999` | **200** | 9,193 | `Jobs` |
| `jobs.smartrecruiters.com/zzzznotrealco999` | **200** | 33,521 | `SmartRecruiters Job Search` |
| `boards.greenhouse.io/airbnb` (real) | 200 | 91,264 | `Positions Archive - Careers at Airbnb` |
| `jobs.ashbyhq.com/openai` (real) | 200 | 119,298 | `OpenAI Jobs` (7× `JobPosting`) |
| `jobs.smartrecruiters.com/Visa` (real) | 200 | 98,587 | `Careers at Visa` |

Ashby and SmartRecruiters return **200 for nonexistent companies**. A status-code-only gate would
write hundreds of fabricated URLs. **Validation must assert content.**

### 4.14 Block rate — measured on a biased sample, do not generalize

Home Depot, Tesla, Caterpillar, Intel returned HTTP 403 on both homepage and `/careers/`, `/jobs/`;
AT&T 403 on homepage but 200 on `/jobs/`; Adobe timed out. **This is 20 hand-picked large,
bot-hostile companies and is not a valid estimate for the S&P 600.** See §8.1 for the real
measurement. Production code must use proper TLS verification and a configurable `User-Agent`; one
exploratory probe used `CERT_NONE` and a spoofed UA for speed only.

---

## 5. Decisions (closed — do not re-litigate without new evidence)

### 5.1 `career_site_url` semantics

Stores the **canonical entry point** regardless of hosting. If a branded subdomain validates, store
that; otherwise store the validated ATS board URL. Do **not** force NULL just because it is
third-party.

A branded careers subdomain (`mycareer.verizon.com`, `careers.nike.com`) counts as **first-party**
for `career_site_url` purposes. The hosting/ATS fact lives in `company_application_platforms`. No
forced binary self-hosted/third-party classification.

`company_application_platforms.base_url` stores the **concrete resolved tenant URL**
(`https://boards.greenhouse.io/airbnb`), **not** the template. The template stays in
`platforms.base_url_pattern`.

### 5.2 NULL beats a fabricated URL

A wrong URL silently poisons the jobs pipeline downstream. Strict validation gate; NULL + attempt
record + retry later.

### 5.3 Audit trail

`url_resolution_attempts` (one row per candidate per run) plus on-disk raw snapshots. **Not** a
multi-row `company_websites` inventory — the user wants one careers URL per company, not a homepage
catalogue.

### 5.4 Identity sources by tier

Wikidata by article title is the primary resolver (99.6%). Tier 2: Wikipedia infobox, for the ~14/16
zero-P856 entities that have articles. Tier 3: **SEC 10-K**, promoted from "last resort" to the
**designated fallback for the ~253 no-article rows** — those rows have CIKs (S&P 500) or SEC links
(400/600), and name search recovers only 40/253 (15.8%) at ~88% precision, so search is not enough.

### 5.5 Milestone split and browser-use deferral

M1 / M2a / M2b / M3 as in §3. browser-use is deferred: it breaks determinism (same task can return
different URLs run to run, so it cannot be on the primary TDD-covered path — only the seam where its
result is consumed is testable) and adds a Python + model runtime to a deliberately cgo-free
single-binary Go project.

### 5.6 ID band scheme

From `002`'s header, keep it: job boards **1–9**, application systems **10–19**, reference sources
**20–29**, additional ATS vendors **30+**. New rows must not collide across bands.

### 5.7 Dual-class tickers

One `companies` row per company; second ticker into `alias`. A `company_tickers(company_id, ticker,
exchange, is_primary)` table is the right long-term shape but is **out of scope for M1/M2**;
flag as a future migration.

### 5.8 Discovery is out of scope

M1/M2 are identity + careers enrichment only. The S&P lists are not hiring signal. If discovery
enters scope later, plan it against the seeded job boards and use the S&P lists only as an
enrichment join.

---

## 6. M1 — implementation spec

### 6.1 Scope

**In:** fetch three pages → raw to disk → parse → upsert `companies` → `scrape_runs` → exit codes.

**Out:** any careers/website resolution, any ATS work, any network beyond the three Wikipedia pages,
`url_resolution_attempts` (that is M2).

M1 is fully deterministic and needs **no live network in tests** — the fixtures are the three
recorded wikitext files.

### 6.2 Migration `004_reference_platforms.sql` — structural only

`platforms.platform_type` must gain `'reference'`, because `scrape_runs.platform_id` is `NOT NULL`
and the run-row-first convention means every Wikipedia run needs a `platforms` row. `'reference'` is
therefore load-bearing, not cosmetic.

SQLite cannot alter a CHECK in place, and the rebuild **fails inside a transaction with FKs ON**:

```
Error: stepping, FOREIGN KEY constraint failed (19)
```

The parent table has child FKs from `company_application_platforms`, `job_listings`, and `scrape_runs`.
Tested-good order (pragma **outside** the transaction):

```sql
-- migrate:fk_off
PRAGMA foreign_keys=OFF;        -- runner executes this OUTSIDE the transaction
BEGIN;
CREATE TABLE platforms_new (
    id INTEGER PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    platform_type TEXT NOT NULL
        CHECK (platform_type IN ('application_system','job_board','reference')),
    base_url_pattern TEXT
);
INSERT INTO platforms_new SELECT * FROM platforms;
DROP TABLE platforms;
ALTER TABLE platforms_new RENAME TO platforms;
COMMIT;
PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;       -- runner asserts zero rows
```

Reference seeds `20–23`:

```sql
(20,'wikipedia_sp500','reference','https://en.wikipedia.org/wiki/List_of_S%26P_500_companies'),
(21,'wikipedia_sp400','reference','https://en.wikipedia.org/wiki/List_of_S%26P_400_companies'),
(22,'wikipedia_sp600','reference','https://en.wikipedia.org/wiki/List_of_S%26P_600_companies'),
(23,'career_resolution','reference',NULL)
```

**No ATS seeds here.** Splitting keeps the FK-off window minimal (one concern per file), and `002`
set the precedent of seeds-in-their-own-migration. The plain `CREATE new / DROP old / RENAME` form
preserves child FK text under `legacy_alter_table=OFF`; it is FK *enforcement*, not reference
rewriting, that blocks the naive form.

### 6.3 Migration `006_company_enrichment.sql`

```sql
ALTER TABLE companies ADD COLUMN ticker TEXT;
ALTER TABLE companies ADD COLUMN cik TEXT;
ALTER TABLE companies ADD COLUMN gics_sub_industry TEXT;
ALTER TABLE companies ADD COLUMN headquarters_location TEXT;
ALTER TABLE companies ADD COLUMN index_membership TEXT;
ALTER TABLE companies ADD COLUMN website TEXT;
ALTER TABLE companies ADD COLUMN website_source TEXT;
ALTER TABLE companies ADD COLUMN career_site_url_source TEXT;
ALTER TABLE companies ADD COLUMN career_site_url_checked_at TEXT;
ALTER TABLE companies ADD COLUMN career_site_url_http_status INTEGER;
ALTER TABLE companies ADD COLUMN career_site_url_title TEXT;
```

`industry` (existing column) holds **GICS Sector**. No migration needed for it.

`index_membership` holds `sp500` / `sp400` / `sp600`. The three lists are disjoint, so a single
column is safe. A company that *moves* between indices gets its value overwritten by the next run;
the change-log tables (`Selected past and announced changes…`) are the historical record and are
**not** parsed in M1.

`cik` stores the raw extracted value. For S&P 400 it may be a ticker; numeric resolution via
`company_tickers.json` is a later step.

**Rejected alternative:** a multi-row `company_websites` table with provenance. Over-engineering —
the user wants one careers URL per company, not a homepage inventory. `url_resolution_attempts`
gives the audit trail without the inventory.

### 6.4 Migration-runner changes (`internal/db`) — M1 scope

`applyMigration` currently does `tx.Exec(body)`. Five additions, each with a test:

1. **Per-migration FK-off directive.** A header comment `-- migrate:fk_off` tells the runner to run
   `PRAGMA foreign_keys=OFF` on the connection **outside** the transaction, then `BEGIN … COMMIT`,
   then `PRAGMA foreign_keys=ON`, then `PRAGMA foreign_key_check` and fail if any rows return.
2. **`foreign_key_check` after every migration**, not just FK-off ones. Catches violations that
   insert-time enforcement misses when the pragma was off. Doc-comment note: this scans every table,
   so if `job_listings` grows into the millions, scope the check. Nearly free at this project's size.
3. **Restore `foreign_keys=ON` on the error path.** `db.Open` caps the pool at `MaxOpenConns(1)`, so
   a connection left with FKs off is the *only* connection and silently disables FK enforcement for
   the process's remaining life. Mandatory, not optional.
4. **Pragma and `Begin` must land on the same physical connection.** Today `MaxOpenConns(1)`
   guarantees it via the pool. If the pool size is ever raised, pragma and transaction can land on
   different connections and the failure is silent (FKs stay on for the transaction's connection,
   and the rebuild fails with the opaque FK error). The runner should hold a single `*sql.Conn`
   for the migration duration (`db.Conn(ctx)`; all pragma/begin/exec on that conn).
5. **Unknown directives must fail loudly.** `-- migrate:fk_off` only helps if a typo
   (`-- migrate:fk-off`) errors the migration rather than being silently ignored as a comment.
   Otherwise the migration runs with FKs on, fails with the opaque FK error, and the author gets no
   signal. Same class of bug as §4.13: silent acceptance of malformed input.

### 6.5 Parser rules

1. **Locate the component table:** depth-count `{|` / `|}` from the first `{|` to the matching close.
   Error if unbalanced. **Any extraction outside that range is a bug** (see §4.4).
2. **Rows:** split on lines starting `|-`. Discard the table-opener block and the header block
   (lines starting `!`).
3. **Data row = a block containing a ticker template.** This is the row-count predicate; it must
   equal 503 / 399 / 600.
4. **Ticker templates:** all six variants from §4.3, including `{{BZX link|X}}`. Match
   case-sensitively and tolerate the `|{{Anchor|...}}` prefix.
5. **Cells:** normalise **both** `||` and newline-`|` delimiters to one sentinel, strip each, **drop
   empties**, then index positionally. Cell 1 = ticker, cell 2 = security name. Dropping empties is
   load-bearing: the positional CIK access in rule 7 depends on it.
6. **Security name:** accept `[[Article|Display]]`, `[[Article]]`, or plain text; strip `<ref>`,
   HTML comments, and nested templates. Plain text is a legitimate outcome (~253 rows), not an error.
7. **CIK, per page:**
   - S&P 500: cell 7, 10-digit zero-padded numeric.
   - S&P 600: the CIK column after SEC filings.
   - S&P 400: no column — extract `CIK=` from the SEC link. Store the raw value as-is; **281 of 399
     values are ticker-like and must not be coerced to integers.**
8. **Slug:** lowercase, `&` → `and`, strip punctuation, collapse internal whitespace to single
   hyphens, trim leading/trailing hyphens. Upsert by slug; dual-class rows (GOOGL + GOOG) collapse to
   one company with the second ticker stored in `alias`.
9. **Counts are independent:** 503 S&P 500 rows yield 503 numeric CIKs but only 500
   security-cell wikilinks. Do not couple them.

### 6.6 Pipeline

Per page, in one process invocation:

1. Load config (env; no flags). Bad value exits `2` with nothing written.
2. Open DB through the existing migration plumbing (applies DSN pragmas, runs pending migrations).
3. Insert `scrape_runs` row with `platform_id` 20 / 21 / 22, `status='running'`, committed
   immediately so a crash is visible as a stuck row.
4. Fetch the page (`action=raw`, configured `User-Agent`, configured timeout).
5. **Write raw bytes to `data/raw/wikipedia/<index>/<YYYY-MM-DD>.wiki` before parsing.** `O_CREATE|O_EXCL`;
   an existing file is never overwritten. A same-day collision still processes the run normally but
   exits `5`.
6. Parse (pure function over bytes).
7. Upsert `companies` in one transaction. Update `updated_at`; preserve `created_at`.
8. Finish the `scrape_runs` row (`finished_at`, `status`, counters).
9. Print one stdout `key=value` line. Exit.

### 6.7 Tests

**Final counts: `503 / 399 / 600`, total `1,502`.**

```
TestMigrationCanRebuildParentTableWithForeignKeyChildren
TestMigrationRunnerAssertsForeignKeyCheckCleanAfterEveryMigration
TestMigrationRunnerRestoresForeignKeysOnError
TestMigrationRunnerHoldsSingleConnectionAcrossPragmaAndTransaction
TestMigrationRunnerRejectsUnknownDirective

TestSP500ParserReturns503SourceRows
TestSP400ParserReturns399SourceRows
TestSP600ParserReturns600SourceRows
TestSP1500ParserReturns1502SourceRows            // 503 + 399 + 600
TestParserIgnoresTickerTemplatesOutsideComponentTable   // the 601/606 regression
TestParserHandlesBzxLinkTemplate                        // CBOE
TestParserHandlesBarePipeCellSyntax                     // |{{Anchor|A}}{{NyseSymbol|AAMI}}
TestParserHandlesStyledCellSyntax                       // | style="..." |
TestParserHandlesNestedTableInCell                      // synthetic fixture
TestFixturesHaveNoNestedTables
TestSP400ParserHandlesPlainTextCompanyNames             // DAR, EGP, KNSL
TestSP500ParserRecordsPlainTextSecurityCell             // BRK.B, BF.B, Insulet
TestExtractsTickerFromAllSixTemplateVariants
TestExtractsCikFromBareNumericCellForSP500
TestSP400StoresTickerLikeCikVerbatimWithoutCoercion     // 281 rows
TestExtractsCikFromSecLinkQueryParamForSP400And600
TestUpsertCompaniesDedupesShareClassesBySlug            // GOOGL+GOOG -> 1 row
TestWriteRawWikitextBeforeParse
TestSPRunWritesScrapeRunOk
TestSameDayRawWikitextCollisionReturnsExit5
TestParserZeroRowsReturnsExit4
```

**Fixtures:** `testdata/wikipedia/sp500.wiki`, `sp400.wiki`, `sp600.wiki` — the captured
`action=raw` bodies, checked in verbatim. Plus three deliberately corrupted variants for negative
tests: truncated mid-table; table bounds absent; a page whose only ticker templates are outside the
component table (the 601 regression).

### 6.8 Commit order

1. **Migration-runner change + its five tests** (own commit). The parser must never be debugged
   against a migration runner that is mid-change.
2. `004` + `006`.
3. Parser + fixtures + assertions.
4. Upsert + `scrape_runs` + exit codes.

---

## 7. M2 / M3 — outline

### 7.1 Resolution ladder

1. **Resolve homepage** for companies with no `website`: Wikipedia infobox first (when an article
   exists) → Wikidata P856 with the §4.7 heuristic → SEC 10-K for the no-article rows (§5.4).
2. **Fetch homepage.** Record raw HTML.
3. **`robots.txt` + `sitemap.xml`** for careers hosts/sitemaps.
4. **Anchor scan** on homepage and same-origin nav pages.
5. **ATS fingerprint and fetch**, split by capability:
   - **5a.** Verified public JSON API — Greenhouse, Lever, Ashby.
   - **5b.** HTML-only tenant page — Oracle Cloud, Eightfold, Workday, Phenom, SuccessFactors, Taleo.
   - **5c.** SmartRecruiters — website unreliable (§4.13); API unverified. Verify with a content gate
     or leave unresolved.
6. **Common-path heuristic** — `/careers`, `/jobs`, `/company/careers`, `/en-us/careers`, …
7. **browser-use worker** — only for residue after all deterministic tiers fail.

Homepage resolution precedes ATS fingerprinting: you cannot guess an ATS slug before you know the
site.

### 7.2 Mandatory content-based validation gate

Never accept on status alone.

- Follow redirects up to 5; record final URL and status.
- Reject on transport failure, non-2xx, 403/429, or non-HTML/non-JSON content type.
- **ATS JSON:** Greenhouse — non-empty `jobs[]`; Lever — non-empty array; Ashby — non-empty `jobs[]`,
  **stream/bound the 13.8MB response**; SmartRecruiters — API only if verified.
- **HTML:** score on company token in title/h1, careers token in title/h1, URL path containing
  `careers`/`jobs`, JSON-LD `JobPosting` count > 0, visible text containing "open positions" /
  "job openings" / "careers".
- **Hard-reject** generic titles `Jobs` and `SmartRecruiters Job Search` when the company token is absent.
- **Hard-reject** decoys: `href="#"`, `trailhead.salesforce.com/career-path/`, investor-relations
  pages, locale storefronts.
- **Hard-reject** redirect-target identity mismatch (§4.8): require the target's label to share a
  token with the security name.
- **Hard-reject** stale single-P856 values that redirect to a subsection path (the Visa case).
- Prefer a branded careers subdomain over an ATS board if both validate.
- If nothing passes: store NULL and record the attempts.

### 7.3 M2 test names

```
TestCareersAnchorScanIgnoresHrefHashDecoy
TestCareersAnchorScanRejectsProductCareerPathPage
TestATSFingerprintMapsWorkdayRedirect
TestATSFingerprintMapsOracleCloudRedirect
TestATSFingerprintMapsEightfoldRedirect
TestValidationGateRejectsAshby200WithoutJobs
TestValidationGateRejectsSmartRecruiters200WithoutCompanyToken
TestValidationGateAcceptsRealATSWithNonEmptyJobs
TestValidationGateRejectsRedirectTargetDifferentCompany
TestBrandedCareersSubdomainPreferredOverATSBoard
TestUnresolvedCompanyStoresNullAndAttempt
TestResolutionRunWritesScrapeRunOk
TestResolutionIdempotentOnSecondRun
```

### 7.4 Migration `007_url_resolution.sql` (M2)

```sql
CREATE TABLE url_resolution_attempts (
    id INTEGER PRIMARY KEY,
    company_id INTEGER NOT NULL REFERENCES companies(id),
    run_id INTEGER REFERENCES scrape_runs(id),
    source TEXT NOT NULL,
    candidate_url TEXT NOT NULL,
    candidate_kind TEXT NOT NULL CHECK (candidate_kind IN ('website','career_site','ats_board')),
    http_status INTEGER,
    final_url TEXT,
    title TEXT,
    validation_status TEXT NOT NULL CHECK (validation_status IN ('accepted','rejected','error')),
    rejection_reason TEXT,
    evidence_path TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
```

`run_id` is nullable, but the ordering trap is real: the `scrape_runs` row must be inserted **first**
(per convention), then attempts.

ATS seed rows `30–34`: `oraclecloud`, `eightfold`, `phenom`, `successfactors`, `taleo`. These are
runtime-discovered vendors, so the M2 migration is where that mapping belongs, not a pure-DDL migration.

### 7.5 M3 — browser-use containment

- Go orchestrator shells out to a Python browser-use worker.
- Worker reads JSON args, writes **one JSON object to stdout**, logs to stderr.
- Go enforces timeout, concurrency 1, and validates the returned URL through the **same content
  gate** before any DB write.
- Feature flag `ENABLE_BROWSER_USE=1`.
- **Fake-worker tests:** success, malformed JSON, timeout, nonzero exit, fabricated URL that fails
  validation.

---

## 8. Measurement plan (run before M3 is justified)

### 8.1 Random 403/timeout sample

Stratified n=300: 100 each from S&P 500/400/600. Equal allocation gives per-index precision; **for
overall rates weight by `(0.3344, 0.2658, 0.3997)`** — otherwise S&P 400 is over-represented ~1.5×.
Report per-index and weighted-overall **separately and labelled**. Fetch homepage + `/careers/` +
`/jobs/` with proper TLS, configurable UA, timeout. Report absolute counts and Wilson 95% CIs.

Also **record a size proxy** per sampled company (market-cap decile, else Wikidata P1128 employee
count, else SEC filer status) and report 403 rate by size decile *within* index. The suspicion is
that bot-hostility tracks company size more than index — testable, and it would justify
re-stratifying.

### 8.2 Wikidata P856 heuristic quality

Run on all ~1,400 matched entities. **Report accuracy with and without the rank rule**, so the Apple
claim is measured rather than asserted. Manually adjudicate a random 100 plus all low-confidence
picks. Report absolute correct/incorrect/ambiguous counts and percentages, plus a 25-company
spot-check table.

### 8.3 browser-use / Ollama action-schema spike

Run the chosen model against the 10 known-correct careers URLs in §4.11. Record schema validity, hit
rate, token and time cost. Test `use_vision=False` against `use_vision=True`.

### 8.4 ATS API validation probe

For Greenhouse / Ashby / Lever / SmartRecruiters: call APIs with **fake and real slugs**. Record
status, bytes, title/body, `jobs[]` length, and whether a content gate would reject the fake. Do not
trust website 200s.

---

## 9. Corrections ledger (do not reintroduce these)

Errors made and falsified during planning. This section exists because each one was caught only by
adversarial re-testing, and several were stated confidently before being overturned.

| Claim | Reality | How it was caught |
| --- | --- | --- |
| Tables contain a website column | No website column on any of the three pages | Direct inspection of all three |
| Row counts `502 / 399 / 600 = 1,501` | `503 / 399 / 600 = **1,502**`; S&P 500 gains CBOE via `{{BZX link}}` | Independent raw template count + page prose ("503 common stocks") |
| `503 + 399 + 600 = 1,503` | = **1,502** | Arithmetic re-check; same class of slip the brief kept catching |
| 15 plain-text rows (an S&P 400 quirk) | **~253 across all pages (1 / 43 / 209)**; these are companies with no article | Full three-page scan; article-existence checks |
| `preferred` rank is a no-op for Apple, so ignore it | Apple is the **outlier**; ~180 entities have a correct `preferred` and 8 spot-checked majors resolved via it | Distribution over all 1,237 entities |
| Whole-page ticker template count is fine | Prose in change-log tables inflates it (bogus 601 / 606) | Scoping extraction to the component table |
| Extraction outside the component table is harmless | It fails by returning a *plausible* wrong number, not an error | Same |
| "16/16 companies found on several ATSs" | False — fake slugs return 200 on Ashby and SmartRecruiters | Re-test with deliberately fake slugs |
| A Lever request was a block | It was a transport-layer failure (empty body, no status) | Distinguishing transport failure from HTTP rejection |
| `'reference'` platform type is a nice-to-have | **Load-bearing** — `scrape_runs.platform_id` is `NOT NULL` | User's argument |
| Oracle Cloud / Eightfold / Phenom have public JSON APIs | No public API found; tier 5b must scrape HTML | Targeted search |
| The `platforms` rebuild works in a transaction | Fails with `FOREIGN KEY constraint failed (19)` | Direct sqlite3 test |
| `PRAGMA foreign_keys=OFF` is ignored inside a transaction | It actually takes effect in this SQLite build; still placed **outside** per the documented 12-step procedure | Direct sqlite3 test |
| `ai.siggy-lab.org` is an internet-exposed Ollama | Resolves to `192.168.50.76` (RFC1918); the probe only worked because this machine is on that network | DNS + address-family check (the error was inferring reachability from probe success) |
| "Qwen3 27B" is not a real tag | It is real: `qwen3.8-27b-64k:latest` | `GET /api/tags` on the live host |
| The model may not do structured actions at all | It emits a valid nested-argument tool call | `POST /api/chat` with a tools schema |

**Recurring failure mode:** treating a derived conclusion as a verified one. Three instances: the row-count
sum, the exposure claim, and the `preferred`-rank generalization. Always re-derive before asserting.

---

## 10. Environment

### 10.1 Model host

```
URL:      https://ai.siggy-lab.org   ->  192.168.50.76 (LAN-private / RFC1918)
Version:  Ollama 0.33.3, fronted by openresty
TLS:      Let's Encrypt wildcard *.siggy-lab.org
Auth:     none (LAN-only; anonymous 200s from inside the network are expected)
```

**Chosen model (user-confirmed): `qwen3.8-27b-64k:latest`**

```
params 27.3B   quant Q4_K_M   size 17.7 GB
native ctx 262144   num_ctx 64440
capabilities: completion, vision, tools, thinking
projector_info present  ->  vision is actually usable
```

Verified: `POST /api/chat` with a `tools` schema returns a structurally correct call —
`{"name":"navigate","arguments":{"url":"https://example.com/careers"}}` — i.e. properly nested
arguments, not the flattened `{"navigate":"..."}` shape the browser-use docs warn about. ~37s wall
for a 128-token generation including model load. This tests **Ollama's native tool API**, not
browser-use's action dialect; the §8.3 spike is still the real test.

Consequence: **`use_vision=True` is the primary config**, not a fallback. 64,440 tokens is tight for
browser-use DOM dumps (20–50k on heavy pages) — `qwen3.8-27b-120k:latest` is the overflow option.

`batiai/qwen3.8-27b:q3` (26.9B, **no vision**) makes a cheap spike control: if the vision variant
performs no better, vision is not earning its context cost.

Client config: `OLLAMA_HOST` — `http://localhost:11434` if the Go binary runs on the NAS,
`https://ai.siggy-lab.org` if it runs elsewhere on the LAN. No auth header. Never disable TLS
verification (exploratory probes used `CERT_NONE`; production code must not).

Set Ollama `keep_alive=-1` for batch runs — the 5-minute default would thrash a long run.

### 10.2 Residual security items (LAN-only, so low severity)

- Confirm **Tailscale Funnel is off** for this service (`tailscale funnel status` on the NAS) — the
  one mechanism that turns a tailnet/LAN service public.
- Confirm **no router port-forward** to `192.168.50.76:443`. Not visible from inside the network.
- Check **tailnet/LAN ACLs** if the network has devices beyond the user's own. `/api/pull` and
  `/api/delete` are unauthenticated and reachable by any device on the network.
- Whether a public A record exists (split-horizon DNS was observed: the name resolves to the private
  address from inside).

---

## 11. Unresolved

1. **Nested-table handling in the table-bounds step** — depth counting (option a) is recommended and
   assumed in §6.5 rule 1. *Closed by default; reopen only if the user prefers the minimal
   "first close + assertion" variant.*
2. **`qwen3.8-27b-64k` vs `-120k` as the M3 default** — 64k as default with 120k overflow, or start
   at 120k and pay the KV-cache cost. M3-only.
3. **Whether any code or CI should point at the model host** — depends on the §10.2 checks, and
   purely M3.

None block M1 or M2a.

---

## 12. Pick up here

1. **Implement M1 commit 1** — migration-runner FK-off support + the five runner tests. Nothing else
   is safe to build until this lands, because `003` cannot apply without it.
2. Then `004` + `006`, then the parser + fixtures + assertions (`503 / 399 / 600 / 1,502`).
3. Before starting M2, run §8.1 and §8.2 — they determine how much of the corpus is unreachable by
   deterministic means, and therefore whether M3 is justified at all.
4. Run §8.3 before committing to the browser-use config.

**Do not re-open §5 without new evidence.** If a measurement contradicts a decision, update this
document's corrections ledger (§9) rather than silently changing course.
