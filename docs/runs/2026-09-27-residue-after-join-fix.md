# Residue resolution after the tier-1 re-keying fixes (2026-09-27)

## Context

Run 6 (`docs/runs/2026-09-26-full.md`) stored 632 careers URLs and left an 866-company residue with
`career_site_url IS NULL`. Of that residue, **562 companies had no homepage at all** (`website IS
NULL`), and §12 of the plan recorded that as the next thing to fix: the largest single lever on the
resolution rate, deterministic, and needing no network round trips to diagnose.

Two independent re-keying bugs were found and fixed:

- `1ca9b6a` — MediaWiki returns a redirected or normalised page under its *target* title and reports
  the mapping separately in `query.normalized` / `query.redirects`. Neither array was decoded, so a
  tier-1 result was filed under the canonical title while the caller looked it up under the requested
  title. The company was then recorded as having no homepage.
- `ea8ab73` — the parser extracts the enwiki title each S&P Security cell links to, but `companies`
  had no column for it, so the write path dropped it and `LoadCompanies` rebuilt the title from the
  company name. The name is usually not the title ("Advanced Micro Devices" links to `[[AMD]]`,
  "Amazon" to `[[Amazon (company)]]`, "Deere & Company" to `[[John Deere]]`).

Both were **measured to be real** before being fixed, against the 562-company residue:

| Cause | Companies | Verified how |
| --- | --- | --- |
| Redirect / normalisation between name and article title | **180** | live `action=query` census with `redirects=1` over all 562 |
| Name finds no article, but the wikilink target resolves | **103 of 106 tested** | querying the link target from the stored raw wikitext |

## Result

```
run_id=11 dry_run=0 companies=866 resolved=202 unresolved=664 skipped=632 failed=0
duration_ms=3540875   (59.0 min, 01:13 -> 02:12 UTC)
stderr: 0 bytes   scrape_runs.status: ok   error_text: NULL
```

`skipped=632` is the pre-existing resolved set excluded by `--refresh-failed`, so
`632 + 866 = 1,498` reconciles. `failed=0`, stderr empty.

| | before | after |
| --- | --- | --- |
| Stored careers URLs | 632 (42.2%) | **834 (55.7%)** |
| Companies with no homepage | 562 | **285** (−49%) |
| Companies with a homepage but no careers found | — | 379 |

The run resolved **202 of 866 (23.3%)** of the residue.

## The gain is attributable to the fix

Of the 202 new career sites, **183 (91%) come from exactly the companies the two bugs were diagnosed
on**:

| Set | Targeted | Now have a homepage | Now have a careers URL |
| --- | --- | --- | --- |
| Aliased name (redirect fix) | 180 | 151 | 102 |
| Name unresolvable, link target resolves | 106 | 100 | 81 |
| **Total** | **286** | **251** | **183** |

The homepage deficit fell from 562 to 285, a drop of 277 against 251 targeted companies that gained a
homepage — the small excess being companies outside the two diagnosed sets that also resolved. New
careers URLs came 725 from `anchor_scan` and 109 from `sitemap`; the tier-1 homepages that fed them
came from `wikipedia_infobox` (159 accepted attempts) and `wikidata_p856` (118).

## What remains, and why it is a different problem

Of the 285 companies still without a homepage:

| | Companies |
| --- | --- |
| No `article_title` at all (the Security cell carried no wikilink) | **242** |
| Has an `article_title`, but it yields no infobox website | 43 |

Only 10 companies with no `article_title` gained a homepage at all, via the name fallback. The 242
are the rows the plan has always routed to the **SEC 10-K tier**, which is deferred: they have no
Wikipedia article to read because the index page never linked one. They are not a bug and no browser
can help them.

Of the 664 still unresolved, the leading rejection reasons point the same way:

```
no_careers_signal   304      locale_only_path      43
forbidden           173      timeout               25
unknown_kind         95      rate_limited          13
```

`forbidden` (173) and `timeout` (25) are the block/retry set that §8.1's browser-use decision rests
on; `no_careers_signal` (304) means a real homepage was fetched and it presented no careers evidence.

## Honest reading

**834 is a count of *stored* values, not of working careers pages.** This distinction is the one the
original run report got wrong, and it is not being repeated here: the 202 values this run added have
**not** been validated by fetching them and classifying the result, so no working-share figure is
claimed. For scale, the 2026-09-27 census of the previous 632 stored values found 529 (35.3%)
loaded as a careers page, 54 provably wrong, and 49 unverifiable — a similar error rate on the new
202 would put the working total near 700 (46–47%).

The next honest step is a validation sweep over the newly stored URLs, not another resolution pass.
A browser-validation sweep (`sp1500 validate`) is being run separately against a copy of the
database. Its verdicts on the 632 pre-existing URLs remain valid - this run only *adds* rows and
rewrites none of those - but it holds no verdict for the 202 companies recovered here, so its
classification of the residue does not yet include them.

## Decisions taken on 2026-09-27

These were first recorded here because §12 of `docs/sp1500-plan.md` could not be edited while another
agent held uncommitted changes to it - a write from a pre-`76d9194` buffer would have reverted the
close-out. That agent has since settled and confirmed it merged on top of this work rather than
reverting it, so **§12 now carries the result and these decisions** and this section is a record
rather than the only copy.

1. **Do not validate the 202 new URLs yet.** 834 therefore stands as an explicitly *stored* count,
   with no working-share figure claimed for the new values. This is a decision, not an oversight -
   the 202 have not been fetched and classified, and the number should not be read as 834 working
   careers pages. (The browser sweep has since validated 634 of the 834; the remaining 200 are
   stored-but-unvalidated, which is the same decision, now stated as a count.)
2. **Do not build the SearXNG tier yet** (Phase A). `SEARXNG_URL` is configured and `format=json`
   works through the reverse proxy, but the client, the `Source='searxng'` value and the M/N decision
   rule are unwritten. Rationale: the join fix recovered 202 companies, and the remaining residue is
   now dominated by 242 rows with no Wikipedia article at all - a set a search engine cannot help
   with either, because there is nothing to search *for*. Revisit only if the 379
   homepage-without-a-careers-page set turns out to be worth attacking.
3. **Validate the recovered URLs before extending the browser-use argument to them.** The existing
   sweep's verdicts on the pre-existing URLs stand unchanged; what it cannot speak to is the
   companies this run recovered, so any §8.1 browser-use conclusion that rests on the size of the
   residue needs revisiting once they are classified.

**This is where the careers-site workstream stops.** The durable dataset is
`data/sp1500-live/jobs.db`; the scratch copy this run wrote, `/tmp/sp1500-live/jobs.db`, has had its
URLs merged into it and is no longer authoritative. The next workstream - fetching job listings from
these URLs - is planned in `docs/scraping-plan.md`.
