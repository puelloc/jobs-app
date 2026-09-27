# Browser validation run (2026-09-27)

The first run of the M3 validation pass: every stored `career_site_url` loaded in a real browser and
judged by `internal/careers.Validate`. This is a *validation* run, not a resolution run - it answers
"is the URL we stored still a careers page?" and never writes `career_site_url`.

## Result

```
sp1500 validate --refresh --concurrency 2
run_id=21 companies=634 skipped=0 confirmed=492 wrong=67 unverifiable=75 escalated=0 failed=0
duration_ms=1449585   (24.2 min)
```

Two bounded correction runs followed it, and the current per-company verdict is the state they leave
behind. Each company's row records which run produced its verdict in
`companies.career_site_url_verdict_run_id`:

| Run | Command | Scope | Outcome | Verdicts it owns |
| --- | --- | --- | --- | --- |
| 21 | `validate --refresh --concurrency 2` | all 634 stored URLs | 492 confirmed, 67 wrong, 75 unverifiable | 566 |
| 22 | `... --only-slugs=<68 affected>` | the 67 wrong + `amgen` | 9 confirmed, 58 wrong, 1 unverifiable | 59 |
| 23 | `... --only-slugs=<9 flipped>` | the 9 that run 22 confirmed | 5 confirmed, 4 wrong | 9 |

**Final, across all 634 stored `career_site_url` values in `jobs.db`:**

| Verdict | Companies |
| --- | ---: |
| **confirmed** - loads and is a job listing or the company's careers landing page | **496** |
| **wrong** - loads and is demonstrably not a careers page | **62** |
| **unverifiable** - no page was obtained, so the URL is neither confirmed nor disproved | **76** |

The 634 is 2 more than the 632 the resolution report recorded: two further resolutions were committed
to the live database after that report was written. Every URL this run judged is byte-identical to the
live `/tmp/sp1500-live/jobs.db`, so the measurement applies to both.

## What the job does

Per company, one Python worker process (`worker/browser_worker.py`):

1. Launch headless Chromium (the cached build 1208; no download) and `GET` the stored URL with
   JavaScript enabled, following redirects.
2. Capture the final URL, HTTP status, content-type, `<title>` and the **rendered DOM** (bounded).
3. Go maps that into a `careers.Response` and judges it with `careers.Validate` - the same gate the
   resolution tiers use. The browser tier has no acceptance rules of its own.
4. Classify: `confirmed`, `wrong`, or `unverifiable`, and write one `url_resolution_attempts` row
   (`source = 'browser_use'`), the company's last-verified stamps, and its verdict.

**`career_site_url` is never written.** The attempts table, the verdict columns and the retained
rendered bodies are the record; the stored URL is the thing being measured.

### The three-way split, and what it means

The definitions are the earlier census's, kept deliberately:

- **confirmed** - the gate accepted the page.
- **wrong** - a page was received and it is demonstrably not a careers page. *This is a claim about the
  company's URL and it is only made from content.*
- **unverifiable** - 403, 406, 5xx, a bot wall, a transport failure or a timeout. The site was not
  read, so nothing is proved. Folding these into "wrong" would turn a blocked host into a false
  accusation, which is the single most expensive mistake this pass could make.

`wrong` is narrower than the earlier census's, which counted an employer-brand page as valid when its
subject was working at the company. Here, a culture/benefits/awards sub-page is `wrong`: it is not a
job listing and not the careers entry point. That is a deliberate definition, not a measurement
difference, and it accounts for part of the gap against the census below.

## Corrections

Four defects were found and fixed; three of them were found by checking this run's own output rather
than by a test.

**1. The generous gate is a discovery rule, not a validation rule.** The first sweep (run 19, generous
profile) reported **555 confirmed / 4 wrong / 75 unverifiable**. Spot-checking its acceptances against
the pages the census had named wrong found **14 of them accepted**: PriceSmart's `JobStar` brush-cutter
product page (`job` inside "JobStar"), a Morningstar "Human Verification" wall answering **202**, an
Uber route page at `.../routes/joinville-le-pont-...` (`join` inside "Joinville"), Dynatrace's
`/hub/detail/control-m-jobs-v2/` product page, and EEO, fraud-alert, supplier and news pages.

The rule was not wrong; its inputs had changed. A discovery tier only ever hands the gate a URL that
already looks like a careers page, so "a path containing `job` counts alone" is a reasonable hint. A
validator hands it any stored URL, and the same hint accepts a stock-quote page for the ticker **JOB**
(`/stocks/xase/job/quote`).

`Candidate.RequireJobListingEvidence` (the validation profile) now:
- rejects a page whose title or first heading names another kind of page (EEO/equal-housing, fraud
  alerts, supplier/partner pages, licence agreements, press releases) - matched on the title, never
  the body, because "equal opportunity" is boilerplate *inside* real careers pages;
- rejects sections by path, split into hard content types (`blog`, `news`, `stories`, `article`,
  `press`, `product`, `route`, `student`) and soft subjects (`culture`, `award`, `benefit`, …) that a
  segment also naming careers overrides;
- requires positive evidence of listing work: JSON-LD `JobPosting`, an openings phrase, or ≥5 links
  to individual postings;
- accepts, as a fallback, the company's own careers landing page (careers-shaped URL or host, a
  careers word or careers path segment, and the company named in the title).

The discovery path's behaviour is unchanged, and a test pins that.

**2. The correction over-reached, and three false acceptances came back.** Run 22 flipped 9 companies
from `wrong` to `confirmed`. Five are real careers pages (`ADM Job Openings`, Equifax and LKQ
`Culture & Careers`, Lindsay `Join Our Team`, Coherent's German `Arbeiten bei Coherent`). Four were
not, and run 23 refused them again:

| Company | Page | Why the rule let it in |
| --- | --- | --- |
| Credit Acceptance | `/dealers/join-our-network` | `join` matched as a *prefix*, so "join-our-network" read as a careers segment |
| Cencora | `/solutions/opportunity-dashboard` | `opportunity` matched as a prefix |
| Masco | `/students-learn-about-stem-careers-at-masco/` | the soft-token rescue skipped the hard `student` token because the slug also contains "careers" |
| Broadridge | a recruiting-event page | accepted by the landing fallback, refused on re-judge |

Segment words are now split: those that may take a suffix (`careers`, `jobs`, `job`, `karriere`, …)
and those that must match the whole segment (`join`, `opportunity`, `employment`, …). Hard path
tokens are checked before the rescue.

**3. A bot wall was recorded as a wrong URL.** Arista's `/en/careers` answered 200 with the title
"Client Challenge", an Imperva/Cloudflare wall. It is now a `bot_challenge` - an unknown, not a
disproved URL - and the Morningstar "Human Verification" 202 is likewise `unverifiable`.

**4. The evidence summary was never written.** `store.ResolutionAttempt.Evidence` has existed since
migration 007 and both the ladder and the validator populate it with the reason a verdict was reached,
including an escalated agent's own reasoning. The `INSERT` predates the field and lists its columns
explicitly, so every value was computed and dropped at the boundary. Migration 012 adds the column and
the writer stores it. This is the plan's silent-aggregation family in a different dress: a field that
looks recorded and is not.

## Per-reason breakdown

`wrong` (62): `no_careers_signal` 35, `product_or_investor_path` 26, `generic_title_without_company` 1.
`unverifiable` (76): `forbidden` 64, `bot_challenge` 6, `transport_error` 3, `http_406` 2, `http_500` 1.

64 of the 634 stored URLs (10.1%) answer a real browser with 403. That is the same blocked residue
§8.1 measured on the resolution side, and it is unchanged by rendering: these are sites that do not
want automated clients, not sites whose URL is wrong.

## The agent tier: one escalated site, measured

The hybrid design runs the browser-use agent only for the residue. One site was escalated, as asked:

```
sp1500 validate --refresh --escalate --dry-run --only-slugs=bank-ozk --agent-timeout 6m
run_id=20 companies=1 confirmed=0 wrong=0 unverifiable=1 escalated=1 failed=0 duration_ms=361240
```

`careers.ozk.com/career-home/` answers 403 to the render. The agent navigated: it tried the base
careers subdomain (403 again), went to `ozk.com`, found the Careers link, followed it back to the same
403, and concluded correctly that the domain is the company's own applicant-tracking host and is
blocked. **Verdict unchanged, 6 minutes spent.**

That is the honest outcome for a hard block, and it is the §8.1 finding re-measured on the validation
side: the blocked residue is diffuse, and a real browser does not beat a per-site bot wall. Escalating
all 76 at ~6 minutes each is ~8 hours for little expected gain, so the agent stays a bounded tool, not
a routine pass.

## Comparison with the plain-client census

| Measurement | Client | Confirmed / valid | Wrong | Unverifiable |
| --- | --- | ---: | ---: | ---: |
| `docs/runs/2026-09-26-full.md` census | curl, no JS | 529 | 54 | 49 |
| First browser sweep (run 19) | Chromium, generous gate | 555 | 4 | 75 |
| **This run (21 + corrections)** | Chromium, validation profile | **496** | **62** | **76** |

Three differences worth naming:

- **Fewer confirmed than the census.** Partly the stricter definition (employer-brand sub-pages count
  as wrong here), partly the stricter evidence rule.
- **More unverifiable (76 vs 49).** A real browser is *more* likely to be blocked than curl on some
  hosts - it presents a full browser fingerprint and runs JavaScript that challenge pages key on.
- **More wrong than run 19 by 58.** That is the correction, not a change in the sites.

## Known limitations

- **A landing page whose title omits the company name can be refused** when the company's distinctive
  token is a descriptor: `Amneal Pharmaceuticals, Inc.` yields `pharmaceuticals`, which appears in
  neither "Join Us in Making Medicines Accessible" nor `amneal.com`. Pinned by a test. Accepting on the
  host alone was tried and reverted - it re-admitted an Amgen news article whose slug contains
  "employment".
- **A rendered page with no `<title>`** (`cnx-resources`) is recorded `wrong` rather than
  `unverifiable`. One company; not worth a new verdict path yet.
- **The agent budget is 6 minutes per site**, which is why escalation is a sample rather than a pass.
- **634 of the live database's 749 stored URLs.** A concurrent `resolve` run added 115 URLs after this
  snapshot. They are unvalidated, and validating them is a separate run against the live database.

## Afterwards: the database was consolidated (2026-09-27)

This run happened against a copy of the S&P 1500 database that a previous session had created at
`/tmp/sp1500-live/jobs.db`. Two things followed it, and they change where these verdicts live:

- The 200 URLs a later resolution run had added were merged in before `/tmp` was wiped.
- The whole S&P dataset was then merged into **`jobs.db` at the repo root** — the database that
  already existed for the RemoteOK feed — with `scrape_runs.id` offset by 1000 (so the runs above are
  1021, 1022, 1023 in that file) and companies rejoined by slug. `internal/db/oneoff/` records how.

The verdicts above are unchanged by the move. What is *not* recoverable from the merged database is
the on-disk evidence for the pre-validation resolution attempts: 1,867 `url_resolution_attempts` rows
have `evidence_path` values under `/tmp/sp1500-live/`, which no longer exists. The 794 rows written by
this validation pass point at `data/sp1500-live/data/raw/` and do resolve. Query the loss directly
with `SELECT count(*) FROM url_resolution_attempts WHERE evidence_path LIKE '/tmp/%'`.

Current tally in the merged database, after a 10-company run that was started by accident and killed:
**503 confirmed / 64 wrong / 77 unverifiable of 644 validated**, with 190 stored URLs unvalidated.

## Method notes

- The verdict columns are current state and are attributed per company by
  `career_site_url_verdict_run_id`; the attempts table is per-run history. A company's verdict is only
  usable for skipping while `career_site_url_verdict_url` still equals its `career_site_url`
  (migration 011) - without that, a resolver repointing a company would leave behind a verdict about a
  page nobody stores, and the company would be skipped forever.
- Accepted pages retain a 512 KB prefix of what the gate saw (`--evidence-bytes`). Rejected pages keep
  everything. This is why the corrections in this report could be re-judged without re-fetching the
  whole board, and why a future rule change should not cost another 25-minute sweep.
- `--refresh` re-validates everything; the **default is to skip companies whose verdict is about the
  URL they store now**. A default run today does no work: `companies=0 skipped=634`.
- Every number above was read from the database after the runs, not from the run's own summary line.
