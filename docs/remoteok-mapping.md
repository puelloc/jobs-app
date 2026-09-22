# RemoteOK → job_listings mapping

## Source facts
- Raw file: `data/raw/remoteok-2026-09-22.json` (sha256: `cf51d6a32f237b9807df065b44aa9bf21059abcbbeda017e976fd0d9cf41a63c`), 624930 bytes
- Total elements: 100
- Legal notice present at index 0: yes; detection rule: it is the only element that is not a job — `.[0] | keys` is `["last_updated","legal"]`, `.[0] | has("id")` is `false`, and it carries no `position`, `company`, `url`, or `epoch`. Element 0 is an object (all 100 elements are objects).
- Job count after skipping notice: 99
- Fields observed across all jobs (union of keys, sorted): `apply_url`, `company`, `company_logo`, `date`, `description`, `epoch`, `id`, `location`, `logo`, `position`, `salary_max`, `salary_min`, `slug`, `tags`, `url`, `original`, `verified`
- Fields present on all jobs: `apply_url`, `company`, `company_logo`, `date`, `description`, `epoch`, `id`, `location`, `logo`, `position`, `salary_max`, `salary_min`, `slug`, `tags`, `url`
- Fields present on some jobs (with counts): `original` 11/99 (boolean, only ever `true`), `verified` 6/99 (boolean, only ever `true`)

Union of keys across *all* elements, including the notice, has two further keys: `last_updated` 1/100 and `legal` 1/100, both on element 0 only.

## Column mapping

| Schema column | RemoteOK source | Transform | Nullable in practice | Confidence |
| --- | --- | --- | --- | --- |
| `id` | — | SQLite assigns the rowid; nothing from the payload goes here | never null (assigned) | certain |
| `company_id` | `company` (indirect) | resolve the `company` string to a `companies.id`, creating the company row if absent; the payload has no company identifier | `company` present and non-empty on all 99 | likely |
| `company_application_platform_id` | — | constant NULL: RemoteOK is the discovery source, not an application system | always NULL | certain |
| `external_id` | `id` | identity (string; 7 digits on all 99) | `id` present and non-empty on all 99 | certain |
| `application_url` | `apply_url` | identity (URL string) | `apply_url` non-empty on all 99 (0 empty, 0 null) | likely |
| `listing_url` | `url` | identity (URL string) | `url` non-empty on all 99 (0 empty, 0 null) | likely |
| `discovery_platform_id` | — | constant 1 (`remoteok`, seed id 1 in 002_seed_platforms.sql) | always 1 | certain |
| `discovery_url` | `url` | identity; byte-identical to `apply_url` on all 99, so this is a duplicate of `listing_url` in this dump | `url` non-empty on all 99 | unsure |
| `title` | `position` | identity (string) | `position` present and non-empty on all 99 | certain |
| `employment_type` | — | constant NULL; no field maps (see `### employment_type`) | always NULL | likely |
| `is_remote` | — | constant 1; RemoteOK is a remote-only board | always 1 | likely |
| `location_text` | `location` | identity (string; free text) | empty string in 34/99; non-empty on 65/99 | likely |
| `country` | — | no clean source; NULL unless a location-parsing decision is made (see `### location`) | always NULL under this mapping | unsure |
| `is_us` | — | no reliable source; NULL (see `### location`) | always NULL under this mapping | unsure |
| `description` | `description` | identity (HTML string, unmodified) | present and non-empty on all 99; min length 543, max 25283 | certain |
| `salary_min_cents` | `salary_min` | multiply by 100 (JSON number, integer-valued on all 99; see `### salary_min / salary_max`) | 0 in 82/99; non-zero in 17/99 | unsure |
| `salary_max_cents` | `salary_max` | multiply by 100 | 0 in 82/99; non-zero in 17/99 | unsure |
| `salary_currency` | — | constant NULL; the payload contains no currency field and no key name contains "currency" | always NULL | certain |
| `salary_period` | — | constant NULL; no period field exists, and `'year'`/`'month'`/`'hour'` cannot be derived reliably (see `### salary_min / salary_max`) | always NULL | unsure |
| `tags_json` | `tags` | JSON-serialize the array, e.g. JSON.stringify(tags) | key present on all 99; empty array in 1/99; null in 0/99 | certain |
| `posted_at` | `epoch` | convert epoch seconds to RFC3339 UTC, e.g. value `1790006411` → `2026-09-21T16:00:11Z` (see `### date vs epoch`) | `epoch` present and non-zero on all 99 | likely |
| `first_seen_at` | — | DB default on insert; never changed afterwards | never null (DB default) | certain |
| `last_seen_at` | — | set to the run timestamp on every observation | never null (app/DB default) | certain |
| `status` | — | constant `'open'` on insert; later flips to `'closed'` via the freshness contract | never null (default `'open'`) | certain |
| `raw_data` | whole element | store the element's raw JSON verbatim (preserves `slug`, `logo`, `company_logo`, `original`, `verified`) | always available | certain |
| `created_at` | — | DB default on insert | never null (DB default) | certain |
| `updated_at` | — | DB default on insert; application code sets it on update (no trigger) | never null (DB default) | certain |

## Fields in RemoteOK with no schema home

| RemoteOK field | Sample value | Why it's being dropped |
| --- | --- | --- |
| `slug` | `"remote-people-operations-coordinator-ashby-1137412"` | Distinct on all 99 and it already ends with `-<id>` on 98/99 (the 99th, id `1136379`, is an empty string). It is a URL component derived from position + company + id, all of which are already stored, so it is **derivable rather than lost**: preserve in `raw_data`. Dropping it entirely would be defensible because it is reconstructible; storing it in `raw_data` costs little and keeps the source evidence intact. `listing_url` already carries the slug form in its path. |
| `company_logo` | `""` (empty string on all 99) | Carries no information in this dump: 99/99 are empty strings, 0/99 non-empty. **Dropped as empty.** If a future run populates it, it is preserved in `raw_data` like every other unmapped key. |
| `logo` | `""` (empty string on all 99) | Same as `company_logo`: 99/99 empty strings, 0/99 non-empty. The prior inspection that reported a `logo` URL was reading a 4000-char truncation that cut off before the value; the untruncated field is empty on every job. **Dropped as empty.** Both logo fields are cheap to carry in `raw_data` and that is the safer default, but neither currently holds a value. |
| `original` | `true` | A boolean flag present on only 11/99 jobs, only ever `true` (never `false`). No schema column exists for a per-source editorial flag. Preserve in `raw_data`. |
| `verified` | `true` | A boolean flag present on only 6/99 jobs, only ever `true` (never `false`). Same reasoning as `original`. Preserve in `raw_data`. |
| `last_updated` | `1790087814` | Appears only on element 0 (the notice), never on a job, so it has no job row to attach to. Preserve in `raw_data` only if the notice element is stored, which this mapping does not do. |
| `legal` | `"API Terms of Service: Please link back (with follow, and without nofollow!) to the URL on Remote OK and mention Remote OK as a source, so we get traffic back from your site. If you do not we'll have to suspend API access.\n\nPlease don't use the Remote OK logo without written permission as it's a registered trademark, please DO use our name Remote OK though."` | Appears only on element 0, which is not a job and gets no row. It is an attribution requirement, not job data. |

## Ambiguous fields (needs a decision before code)

### salary_min / salary_max

- What we see in the dump:
  - Both are JSON numbers, integer-valued on all 99 (`salary_min` type histogram: `number` 99/99).
  - 8 raw examples mixed across the range, with ids:
    - `{"id":"1137411","salary_min":0,"salary_max":0}` (all-zero case)
    - `{"id":"1137155","salary_min":30,"salary_max":36}`
    - `{"id":"1137139","salary_min":10000,"salary_max":750000}`
    - `{"id":"1137307","salary_min":60000,"salary_max":80000}`
    - `{"id":"1137399","salary_min":150000,"salary_max":250000}`
    - `{"id":"1136795","salary_min":190000,"salary_max":220000}`
    - `{"id":"1137407","salary_min":300000,"salary_max":340000}`
    - `{"id":"1136796","salary_min":150000,"salary_max":185000}`
  - Fraction that are 0: `salary_min` 82/99, `salary_max` 82/99. They are zero together: 82/99 (`salary_min == 0 and salary_max == 0`). There are 0/99 rows where exactly one of the two is zero.
  - Fraction where min == max: 83/99, but 82 of those are the both-zero rows, so only 1/99 has a non-zero min equal to a non-zero max (`{"id":"1137309","salary_min":20000,"salary_max":20000}`). 0/99 have `salary_min > salary_max`.
  - Fraction that look annual ($100k+) vs hourly ($50–200), measured on `salary_max`: 10/99 have `salary_max >= 100000`; 1/99 has `salary_max` in 1..500 (the `30`/`36` row); 6/99 fall between 501 and 99999; 82/99 are 0.
  - The full set of distinct non-zero `salary_max` values: `[36, 20000, 40000, 70000, 80000, 150000, 185000, 200000, 220000, 250000, 340000, 750000]`.
  - Duplicated pairs: `10000-750000` occurs 3 times (ids `1137136`, `1137138`, `1137139`, all company `Interaction Design Foundation`); every other non-zero pair occurs once.
  - Correlation with tags or location: no pattern is visible. All 17 non-zero rows have empty `location` except three (`Redwood City` twice, `Boston` once, `New York City` once in the wider set); the hourly-looking row (`30`/`36`) carries `full time` and `senior` tags; the `10000`/`750000` rows carry `education`, `design`, `content writing`. The 82 zero rows are spread across the whole tag distribution. Nothing in the tags distinguishes hourly from annual.
- Possible interpretations:
  1. Values are whole currency units (dollars) with no period attached; `0` means "not specified"; the one `30`/`36` row is an hourly rate and everything above 1000 is annual. Under this reading the whole-number values are dollars and the transform to `salary_min_cents` is a plain `* 100`.
  2. Values are already minor units (cents), which would make `300000` mean $3,000 and `750000` mean $7,500 — implausible as annual pay, and it would make the `30`/`36` row 30 cents.
  3. `0` is a literal "zero dollars" rather than "unspecified", which would make 82/99 jobs explicitly unpaid — implausible on a paid job board, but the dump cannot prove otherwise.
- What we cannot determine from the dump alone: the currency (no `salary_currency` field, no key containing "currency", no currency symbol embedded in the numbers), and the pay period for any given row. There is no field that states "per year" or "per hour". The magnitude heuristic splits 17 non-zero rows into 16 annual-looking and 1 hourly-looking, but that is inference, not data.
- What to check to resolve it: fetch a job whose salary is advertised on its own company page (follow `apply_url` for ids `1137155` and, say, `1137407`) and compare the published figures and period to the JSON numbers; this settles dollars-vs-cents, the period, and the meaning of `0` in one pass.

### date vs epoch

- What we see in the dump: both fields exist on all 99 jobs and agree exactly. Comparing `epoch | todate` against `date` with `+00:00` normalized to `Z` yields **0 mismatches out of 99**:
  - `id=1137412  date=2026-09-21T16:00:11+00:00  epoch=1790006411  epoch|todate=2026-09-21T16:00:11Z`
  - `id=1137411  date=2026-09-20T00:00:31+00:00  epoch=1789862431  epoch|todate=2026-09-20T00:00:31Z`
  - `id=1137410  date=2026-09-20T00:00:25+00:00  epoch=1789862425  epoch|todate=2026-09-20T00:00:25Z`
  - `id=1137409  date=2026-09-19T20:00:01+00:00  epoch=1789848001  epoch|todate=2026-09-19T20:00:01Z`
  - `id=1137408  date=2026-09-19T08:00:34+00:00  epoch=1789804834  epoch|todate=2026-09-19T08:00:34Z`
  - `date` matches `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}[+-][0-9]{2}:[0-9]{2}$` on 99/99 (0 exceptions), and its UTC offset is `+00:00` on 99/99.
  - `epoch` is a JSON number on 99/99, spanning 1785372050 (`2026-07-30T00:40:50Z`) to 1790006411 (`2026-09-21T16:00:11Z`).
- Possible interpretations:
  1. `epoch` is the canonical value and `date` is a display rendering of it; use `epoch`.
  2. `date` is canonical and `epoch` is derived; use `date` (already UTC, `+00:00`).
  3. Both come from the same upstream value, so either works and the choice is about parse robustness.
- What we cannot determine from the dump alone: which field the source treats as authoritative, and whether they can ever disagree. With one run and zero observed disagreement there is no evidence of divergence — and equally no evidence that they *cannot* diverge. `date` also carries no sub-second precision and its offset is uniform here, so a future non-UTC offset would change the parse.
- What to check to resolve it: examine a second run's payload and re-run the exact comparison; if the two fields still agree on all elements, the choice is free and should be made on parse simplicity.

### location

- What we see in the dump: `location` is a string on 99/99 jobs. 34/99 are empty strings (`""`). Top 20 values with counts (empty shown as `[]`):
  - 34 `[]`, 4 `[Remote]`, 2 `[Austin, Austin, Texas, United States]`, 2 `[Boston]`, 2 `[California, California, United States]`, 2 `[Redwood City]`, 2 `[Remote - US]`, 2 `[Remoto]`, 2 `[United States]`, 1 `[Agra, ]`, 1 `[Alice Springs, ]`, 1 `[Bangkok]`, 1 `[Bishkek, Bishkek, Bishkek City, Kyrgyzstan]`, 1 `[Black Bess, ]`, 1 `[Brisbane City, ]`, 1 `[Budapest, ]`, 1 `[Bury St Edmunds, ]`, 1 `[Chennai, Chennai, Tamil Nadu, India]`, 1 `[Cincinnati]`, 1 `[Dallas, Dallas, Texas, United States]`
  - Empty strings: 34/99.
  - Matching `(?i)\b(USA|U\.S\.A|United States|US)\b`: 12/99. Those 12 verbatim are `[United States]` ×2, `[Remote - US]` ×2, `[Select USA Remote Locations]`, `[New York, New York, New York, United States]`, `[Austin, Austin, Texas, United States]` ×2, `[Dallas, Dallas, Texas, United States]`, `[California, California, United States]` ×2, `[San Diego, San Diego, California, United States]`.
  - Matching `(?i)worldwide|anywhere|global|remote|remoto|latam`: 12/99 — `[Remote]` ×4, `[Remote - US]` ×2, `[Remoto]` ×2, `[Mexico City, Mexico - Remote]`, `[Select USA Remote Locations]`, `[LATAM]`, `[Remote UK]`. Note the string `Worldwide` itself appears 0/99; the count above is "worldwide or similar" and is dominated by the word `Remote`, which overlaps the US set.
  - Specific non-US countries appear as city/province/country tuples: `[Bishkek, Bishkek, Bishkek City, Kyrgyzstan]`, `[Chennai, Chennai, Tamil Nadu, India]`, `[India]`, `[Ireland]`, `[Kuala Lumpur, Kuala Lumpur, Wilayah Persekutuan Kuala Lumpur, Malaysia]`, `[London, London, England, United Kingdom]`, `[Seoul]`, `[South Korea]`, `[Sunshine Coast, Sunshine Coast, Queensland, Australia]`, `[Vancouver, BC, Canada]`, `[Vilnius, Vilnius, Vilniaus, Lithuania]`, `[Budapest, ]`, plus three wholly non-Latin-script values (Arabic, listed below under Observations).
  - Format is inconsistent: some values are a single city (`Boston`), some a full `City, City, Region, Country` tuple with duplication (`Austin, Austin, Texas, United States`), some end with a trailing `", "` and nothing after it (`Agra, `, `Texas, `, `Treliske, `-style), and some are markers rather than places (`Remote`, `Remoto`, `LATAM`).
  - Mojibake is present in location values too: `[Islamabad, Islamabad, IslÄmÄbÄd, Pakistan]`.
- Possible interpretations:
  1. `location_text` holds the value verbatim and `country`/`is_us` stay NULL for this source, because no field states a country.
  2. `country` and `is_us` are derived by parsing the last comma-separated component, which works for `..., United States` and `[India]` but fails for `[Boston]`, `[Redwood City]`, `[Remote]`, `[LATAM]`, and all 34 empty values.
  3. `location` is treated as a remote-eligibility marker as much as a place, so `Remote - US` and `Select USA Remote Locations` describe eligibility rather than an office.
- What we cannot determine from the dump alone: whether a location names where the employer sits, where the candidate must sit, or both; whether an empty string means "unrestricted" or "unstated"; and whether `Remote - US` means US-only. 34/99 empty values make any parse leave a large NULL population.
- What to check to resolve it: compare a handful of parsed locations against the same jobs' `description` text (which frequently states eligibility) to see whether the location token or the description is the better eligibility signal — but note that any such parse is a classifier, which this document does not propose.

### apply_url vs url

- What we see in the dump:
  - `apply_url == url` on **99/99** jobs. There are **0** jobs where they differ, so no differing example can be shown.
  - `apply_url` is an empty string on 0/99; `url` is an empty string on 0/99. Neither is ever null or missing.
  - Both fields are absolute `https://` URLs on 99/99, and the host is `remoteOK.com` (capital O-K) on 99/99 for both fields.
  - Format: `https://remoteOK.com/remote-jobs/<slug>` where `<slug>` ends with `-<id>`, e.g. `https://remoteOK.com/remote-jobs/remote-people-operations-coordinator-ashby-1137412`.
- Possible interpretations:
  1. RemoteOK sets `apply_url` to its own redirect page, meaning it is not a direct application endpoint; a real application URL would have to come from following the link or from an application-system row.
  2. `apply_url` is simply a duplicate field kept for API compatibility, and the correct application target is elsewhere in the element (nothing in this dump carries an external ATS host, so this is unproven).
  3. Both are the same because every listing is hosted on RemoteOK in this sample.
- What we cannot determine from the dump alone: whether `apply_url` ever points somewhere other than `remoteOK.com`, since it does not in these 99 rows. The distinction between "where we scraped" and "where you submit" is therefore unobservable here — every candidate value is the same string on the same host.
- What to check to resolve it: issue a single request to one `apply_url` and observe whether it returns a redirect to an external ATS host; if it does, `application_url` should be the redirect target rather than the RemoteOK URL.

### discovery_url

- What we see in the dump: there is no `discovery_url` key in the payload. The only candidate source is `url`, which is byte-identical to `apply_url` on 99/99 jobs, host `remoteOK.com` on 99/99, and never empty. Element 0 carries no URL at all.
- Possible interpretations:
  1. `discovery_url` and `listing_url` both come from `url` and are therefore identical on every row, making `discovery_url` redundant for this source.
  2. `listing_url` takes `url` and `discovery_url` stays NULL, on the grounds that the discovery page and the listing page are the same document rather than two URLs.
  3. `discovery_url` is reserved for the case where a job is found on one platform and listed on another, which does not occur in this dump because there is only one source.
- What we cannot determine from the dump alone: whether RemoteOK ever exposes a distinct discovery page separate from the listing URL. With one source and one URL field, the two schema columns have exactly one candidate value between them, so the dump cannot show the distinction the columns exist for.
- What to check to resolve it: decide whether `discovery_url` is meant to differ from `listing_url` for board-sourced rows at all, and if it is not, confirm that storing the same value in both columns is intended rather than an accident.

### tags

- What we see in the dump:
  - `tags` is present on 99/99. It is `null` on 0/99. It is an array on 99/99 and a non-array on 0/99.
  - Empty arrays: 1/99. Non-string elements inside arrays: 0.
  - Tag counts per job: min 0, max 46, sum 1266, mean 12.79, median 11. The full histogram (count → jobs): 0→1, 1→1, 2→3, 3→9, 4→3, 5→9, 6→5, 7→4, 8→3, 9→5, 10→5, 11→4, 12→4, 13→6, 14→9, 15→1, 16→2, 17→3, 18→4, 19→2, 21→2, 22→1, 23→1, 24→1, 25→1, 30→2, 32→2, 33→1, 34→1, 39→1, 40→1, 42→1, 46→1.
  - Distinct tags: 122. Top 20 with counts: `exec` 68, `digital nomad` 57, `customer support` 53, `marketing` 50, `dev` 46, `ops` 44, `design` 42, `engineer` 40, `technical` 39, `education` 36, `medical` 34, `finance` 32, `golang` 30, `sales` 29, `senior` 28, `content writing` 27, `sys admin` 26, `full time` 25, `infosec` 24, `recruiter` 23.
  - Category-like tags rather than skills: the high-frequency entries `exec`, `digital nomad`, `customer support`, `marketing`, `dev`, `ops`, `design`, `engineer`, `technical`, `education`, `medical`, `finance`, `sales`, `sys admin`, `full time`, `infosec`, `recruiter` read as board departments or working arrangements, whereas `golang`, `c sharp`, `xamarin`, `typescript`, `angular`, `shopify`, `salesforce` read as concrete skills. A single job can carry both kinds: id `1137411` has 25 tags combining `dev`, `exec`, `senior`, `digital nomad` with `c sharp`, `xamarin`, `angular`.
  - Employment-arrangement tags exist among them: `full time` on 25/99 jobs, `part time` on 6/99. `contract`, `intern`, and `temporary` appear on 0/99 each.
- Possible interpretations:
  1. `tags` is a flat, unordered mixture of board categories, seniority, work arrangement, and skills, and should be stored as-is with no interpretation.
  2. The category-like subset is board taxonomy rather than job attributes, and mixing it with skills in one array makes tag-based search noisy.
  3. The arrangement tags (`full time`, `part time`) are the intended source for `employment_type` even though the schema has a dedicated column with a CHECK constraint.
- What we cannot determine from the dump alone: whether a missing tag means "not applicable", "unknown", or simply "not tagged"; whether the array order is meaningful (it is not alphabetical and not obviously ranked); and whether tags are assigned by the employer or by RemoteOK. The 0/99 counts for `contract`/`intern` also make it impossible to tell whether those values would use the same spelling as the schema's `contract`/`intern` enums.
- What to check to resolve it: confirm with the source's own field documentation whether `tags` is employer-supplied or board-assigned, and check a second run for whether the occasional `contract`/`intern` spellings appear — until then `employment_type` stays NULL.

### id

- What we see in the dump:
  - Type is `string` on 99/99 jobs (`.[1:][] | .id | type` → only `string`). Absent entirely from element 0, which has keys `["last_updated","legal"]` and `has("id") == false`.
  - All 99 are numeric-only strings matching `^[0-9]+$`: 0 jobs have non-numeric characters. Length is exactly 7 on all 99 (min 7, max 7).
  - Distinct ids: 99 out of 99 values. No two jobs share an id — `group_by(.) | map(select(length>1))` is empty.
  - Range observed: smallest `1135675`, largest `1137412`. `epoch` and `date` ascend with `id`: the smallest id (`1135675`) has the earliest `epoch` (1785372050 = `2026-07-30T00:40:50Z`) and the largest id (`1137412`) has the latest (`1790006411` = `2026-09-21T16:00:11Z`).
- Possible interpretations:
  1. `id` is RemoteOK's internal listing id, unique and stable per listing, and suitable as `external_id` as-is.
  2. The string type is incidental (JSON-encoded number) so it could be normalized to an integer, but doing so would lose leading-zero safety and is unnecessary since `external_id` is TEXT.
  3. The id identifies a *listing* (a posting event) rather than a logical job, so the same underlying role reposted would receive a new id.
- What we cannot determine from the dump alone: whether ids are stable across runs. With a single sample there is no second observation to compare against — cannot determine from one sample; would need run 2. Nothing in this dump shows an id changing or repeating, and nothing shows whether an edited listing keeps its id.
- What to check to resolve it: re-run the scraper on a later date and diff the id sets, then check whether any id from this sample persists with a different `slug`, `position`, or `epoch`.

### description

- What we see in the dump:
  - Present on 99/99, empty string on 0/99.
  - Length: min 543, max 25283, mean 5387.94. 10/99 exceed 10000 characters; 3/99 exceed 20000. The longest is id `1136370` (`PFAS (Personal Functional Assessment Services)`) at 25283 characters. This is far below any practical SQLite TEXT limit.
  - Always HTML: 99/99 contain at least one tag matching `<[a-zA-Z/][^>]*>`; 0/99 contain no `<` character at all, so there are no plain-text descriptions.
  - Suspicious encodings are widespread: 78/99 descriptions contain the character `â`, 31/99 contain `Â`, 9/99 contain `Ã`, and 1/99 contains `Ã¢`. The pattern is UTF-8 bytes decoded as Latin-1, e.g. `Hi! Iâm Hannah` where the source byte sequence is U+2019.
  - The same defect reaches other fields: `position` on id `1136379` is `Sales Development Representative Attributeâ`, and `location` on one job is `Islamabad, Islamabad, IslÄmÄbÄd, Pakistan`.
- Possible interpretations:
  1. The mojibake is introduced by RemoteOK's own JSON encoding, so every consumer sees it and the fix belongs on our side (re-encode Latin-1 back to UTF-8, or repair known sequences) after storage.
  2. The defect is introduced by the scraper or its HTTP client, meaning the bytes on disk are not what the server sent — contradicted by the raw file itself containing the pattern verbatim, but not disproved for the server side of the exchange.
  3. The descriptions are stored as-is with mojibake intact and repaired only for display or search indexing.
- What we cannot determine from the dump alone: whether the server's bytes already contained the mojibake or whether the damage happened in transit/storage. The raw capture is the only evidence, and it shows the pattern, but a raw capture cannot prove what the server intended. The 78/99 rate also makes per-string manual repair impractical without a general rule.
- What to check to resolve it: request one affected listing directly (ids `1137412` or `1136379`) with `curl` and inspect the raw bytes for `â` versus the proper UTF-8 sequence, comparing byte-for-byte against what the scraper stored.

### employment_type

- What we see in the dump: there is no `employment_type` field, and no key whose name contains `type`, `employment`, `contract`, `schedule`, or `commitment`. The only related evidence is inside `tags`: `full time` on 25/99 jobs, `part time` on 6/99, and `contract`, `intern`, `temporary` on 0/99 each. The `description` of some jobs mentions employment terms in prose, but that is unstructured text.
- Possible interpretations:
  1. `employment_type` stays NULL for every RemoteOK row, because the payload never states it as data.
  2. Derive it from the `full time` / `part time` tags, mapping `full time` to `full_time` and `part time` to `part_time` (the schema's enum spelling differs from the tag spelling).
  3. Derive it from description prose, which would require text classification.
- What we cannot determine from the dump alone: whether a missing arrangement tag means the board never publishes the arrangement or means the specific listing omitted it; the 0/99 counts for `contract`/`intern` give no evidence about how those would be spelled; and tag presence cannot distinguish "full-time only" from "full-time preferred".
- What to check to resolve it: compare the tags of jobs whose descriptions state a contract or internship against the tags of jobs with no arrangement tag, to see whether the tag set is a complete or partial signal before relying on it.

### posted_at

- What we see in the dump: two candidate fields, both present on 99/99 and in exact agreement (0 mismatches, see `### date vs epoch`).
- Possible interpretations:
  1. Use `epoch` (integer seconds, unambiguous, no offset parsing).
  2. Use `date` (already UTC with `+00:00`, but a string needing a parser).
  3. Store both — `posted_at` from one and the other in `raw_data`.
- What we cannot determine from the dump alone: whether `epoch` is always an integer rather than a float or string on other runs; this sample shows `number` on 99/99 but one run cannot establish the invariant.
- What to check to resolve it: on a second run, verify the `epoch` type histogram is still `number` 100% before committing to an integer parse.

### country / is_us / location_text

- What we see in the dump: no field names a country or a US flag. `location` carries country names inside free text on some rows (`United States` 12/99 by pattern, plus `India`, `Ireland`, `South Korea`, `Canada`, `Malaysia`, `Lithuania`, `Kyrgyzstan`, `United Kingdom`, `Australia`, `Pakistan`), and is an empty string on 34/99.
- Possible interpretations:
  1. `location_text` gets the raw string, `country` and `is_us` stay NULL for this source.
  2. `country` is parsed from the location tuple's trailing component, leaving NULL wherever no country appears — which is most rows, given 34 empty values and many single-city values.
  3. `is_us` is inferred from any US token in `location`, which would mark 12/99 as US and leave 87/99 unknown, including 34 known-nothing rows.
- What we cannot determine from the dump alone: whether the location describes employer or candidate eligibility, and whether `Remote - US` should count as a US job. The 34/99 empty values mean any derived column would be a partial signal with no way, from this dump, to distinguish "not US" from "not stated".
- What to check to resolve it: determine whether RemoteOK publishes any structured country field at all by checking its documentation, before deciding whether `country`/`is_us` are derivable or permanently NULL for this source.

## Company name handling
- Distinct company strings in the dump: 91 (from 99 values; 8 rows repeat a company already seen).
- Companies appearing more than once: `Bjak ` 3, `Interaction Design Foundation` 3, `Delinea` 2, `GROW10X` 2, `Stone` 2, `iMerit Technology` 2.
- 15 examples verbatim:
  - `[AWeber]`
  - `[Ace IT Careers]`
  - `[Adaptive Teams]`
  - `[Airspace Link]`
  - `[American Bureau of Shipping (ABS)]`
  - `[Arabian Private Holdings]`
  - `[Arango]`
  - `[Ashby]`
  - `[BMWL]`
  - `[Balco, Inc.]`
  - `[Benchling]`
  - `[Benzinga]`
  - `[Berni and Mick Ireland]`
  - `[Bjak ]`
  - `[Blackbird Interactive]`
- Weird casing observed: `AWeber` (internal capital), `BMWL` (all caps), `iMerit Technology` (lowercase leading letter), `GROW10X` (digits and caps).
- Suffixes and punctuation observed: `Balco, Inc.`, `Crystalia Glass LLC`, `Sophie's Flats Inc.`, `Total Environmental Concepts Pty ltd` (lowercase `ltd`), `Webnotics Pvt Ltd`, `Law Offices of Sabrina Li`, `American Bureau of Shipping (ABS)` (parenthesised acronym), `Kruger NearShore LLC - Rekluti` (suffix plus a trailing segment after a dash), `OrderYOYO`, `Tessera Labs`.
- Leading/trailing whitespace: `Bjak ` has a trailing space, on 3 rows (ids `1137410`, `1137388`, `1136670`). These are the only whitespace-padded company values in the dump. No company value has leading whitespace.
- Non-ASCII: 0 companies contain non-ASCII characters after the trailing-space case above is excluded, so no unicode normalization is needed for `company` in this sample. (Non-ASCII and mojibake *are* present in `location` and `description`.)
- Naive-slugification collisions: comparing all 91 distinct values after lowercasing and stripping every non-alphanumeric character yields **no** groups of two or more, so nothing in this dump collides under that normalization. The near-collisions worth watching are cosmetic rather than actual — the suffix forms `Balco, Inc.`, `Crystalia Glass LLC`, `Sophie's Flats Inc.`, `Total Environmental Concepts Pty ltd`, `Webnotics Pvt Ltd` would each collapse to a shorter slug once the suffix is stripped, but no second company in this dump shares their stem. `Bjak ` and a hypothetical `Bjak` would collide, and the trailing space is the only reason this dump avoids it.

## Timestamp handling

| Field | Format | Timezone | UTC? | Round-trips to RFC3339 UTC? | Raw examples |
| --- | --- | --- | --- | --- | --- |
| `epoch` | JSON number, integer seconds since Unix epoch | UTC by definition | yes | yes, directly: `1790006411` → `2026-09-21T16:00:11Z`; observed range 1785372050 (`2026-07-30T00:40:50Z`) to 1790006411 (`2026-09-21T16:00:11Z`) | `1790006411`, `1789862431`, `1789848001` |
| `date` | string, `YYYY-MM-DDTHH:MM:SS+HH:MM`, second precision | offset explicitly `+00:00` on 99/99 | yes | yes, after normalizing `+00:00` to `Z`; textually already ISO8601 | `"2026-09-21T16:00:11+00:00"`, `"2026-09-20T00:00:31+00:00"`, `"2026-09-19T20:00:01+00:00"` |
| `last_updated` (notice only, element 0) | JSON number, integer seconds since Unix epoch | UTC by definition | yes | yes: `1790087814` → `2026-09-22T14:36:54Z` | `1790087814` |

`date` and `epoch` agree exactly on all 99 jobs (0 mismatches), so either is a faithful representation of the same instant. Both round-trip cleanly into the schema's RFC3339 UTC TEXT format. No other timestamp-bearing field exists in the payload — `slug` ends with the numeric id, not a date.

Decision for `posted_at`: use `epoch`, formatted as RFC3339 UTC with a `Z` suffix (`2026-09-21T16:00:11Z`). `epoch` is an integer with no offset or precision ambiguity, so it cannot be mis-parsed the way a string with a varying offset could, and it needs no string manipulation. `date` is retained in `raw_data` for cross-checking. The confidence on this column is `likely`, not `certain`, because a single sample cannot prove `epoch` is always an integer.

## Idempotency implications

The invariant from `docs/scraper-design.md`: running the scraper twice in a row creates zero duplicate job rows, upserting on the partial unique index `unique_job_listings_by_discovery` with `discovery_platform_id = 1` and `external_id` taken from the payload.

- **Field that becomes `external_id`:** `id`, the 7-digit numeric string, used as-is with no transformation. It is present, non-empty, numeric-only, and distinct on 99/99 jobs.
- **What happens when a job element has no id:** it is skipped and recorded as a per-element normalization problem; it is *not* inserted with a NULL `external_id`, because NULL would fall outside the partial index and silently disable dedupe. In this dump only element 0 lacks an `id`, and element 0 is the legal notice rather than a job, so no job row is lost by this rule. No other element lacks `id`. Because a skipped element is also absent from the seen set, the two sets stay consistent.
- **What happens when two job elements have the same id:** the second would upsert onto the first, so the run would write one row and count one insert plus one update rather than two inserts. This does not occur in this dump — 99 of 99 ids are distinct — so the rule is defensive here rather than exercised.
- **Columns updated on conflict:** `last_seen_at`, `updated_at`, `status` (to `'open'`), `raw_data`, and the re-parsed content columns `title`, `listing_url`, `application_url`, `discovery_url`, `external_id`, `employment_type`, `is_remote`, `location_text`, `country`, `is_us`, `description`, `salary_min_cents`, `salary_max_cents`, `salary_currency`, `salary_period`, `tags_json`, `posted_at`.
- **Columns preserved on conflict:** `id`, `first_seen_at`, and `created_at` are never touched by an upsert. `company_id` is re-resolved and may be repointed if the company string changes.
- **Freshness interaction:** because closure is decided by set membership in the run's seen set rather than by timestamps, a job that fails normalization on a later run is not closed — it stays in whatever state it had. Nothing about this dump changes that contract.

## Schema concerns
- `salary_currency` has no source in this payload and no key name contains "currency", so every RemoteOK row would carry NULL in it. Evidence: `salary_currency` appears in `001_init.sql` at line 58, while the union of payload keys contains none of `salary_currency`, `currency`, or any key with "currency" in the name. This is dead weight for this source, and whether it is reserved for other sources is a decision the schema's own comment already implies.
- `country` and `is_us` likewise have no reliable source: the only geographic signal is the free-text `location`, which is an empty string on 34/99 jobs and mixes cities, regions, and countries in an inconsistent shape (`Boston`, `Austin, Austin, Texas, United States`, `Agra, `). Both columns would be NULL or a partial inference for this source. The mapping does not change to accommodate them, and the schema does not need to change either — but the columns will be unpopulated.
- There is no column for the `original` and `verified` flags, which are present on 11/99 and 6/99 jobs respectively and are always `true` when present. `raw_data` is the only home for them. Similarly there is no column for `slug`, which is distinct on 99/99 values.
- `is_remote` cannot be sourced from the payload at all, in either direction: no element carries a remote flag, and all 99 jobs come from a remote-only board. The column will be a constant 1 for this source, which is a value the mapping asserts rather than one the data provides.
- The RemoteOK `salary_min`/`salary_max` values are JSON numbers whose period is not stated and are 0 on 82/99 jobs, while the schema's `salary_min_cents`/`salary_max_cents` are INTEGER cents and `salary_period` has a CHECK constraint limited to `'year'`, `'month'`, `'hour'`. Nothing in the payload selects among those three, so `salary_period` cannot be populated without inference. All three columns are marked `unsure` in the mapping table for this reason.
- `description` is stored as `TEXT` and the maximum observed length is 25283 characters, well within SQLite's limits, so no concern there. The real quality issue is the widespread mojibake (78/99 descriptions), which is a data-repair matter rather than a schema one.
- `tags_json` being `TEXT` with `json_valid` is sufficient to store this dump's `tags` shape: all 99 values are arrays of strings, with 0 non-string elements and 1 empty array. No evidence in this dump argues for a different representation.

## Open questions for implementation
- Do we need a second run to prove `id` stability across runs, and can that run be timed to also test whether a re-posted listing keeps its id?
- The response contains exactly 100 elements while the file is 624930 bytes — is 100 a hard cap on this endpoint, and if so does a query parameter or a different route exist to page beyond it?
- Should `slug` be written into `raw_data`, or is it genuinely droppable given it already ends with `-<id>` on 98/99 jobs and `listing_url` embeds the same string?
- Is `apply_url` ever a non-`remoteOK.com` host, or does it always point back at RemoteOK's own listing page, and does following it produce a redirect to an external ATS?
- Should `application_url` be populated from `apply_url` at all when the two fields are byte-identical on 99/99 jobs, or should it stay NULL until a real application URL is resolvable?
- Which field is authoritative for `posted_at` — `epoch` or `date` — given they currently agree on 99/99?
- Are `full time` (25/99) and `part time` (6/99) in `tags` the intended source for `employment_type`, or does `employment_type` remain NULL for this source?
- Is the mojibake in `description`, `position`, and `location` introduced by RemoteOK's encoding or by our capture path, and if it is repaired, is it repaired at write time or at read time?
- Does a missing `original` or `verified` key mean `false`, or does it mean the flag was never evaluated for that listing?
- Is a `salary_min`/`salary_max` of 0 "not specified" or a genuine zero, and what currency and period should the non-zero values be interpreted with?
- What does an empty `location` mean — unrestricted, unstated, or remote-by-default — and does `Remote - US` describe candidate eligibility or employer location?
- Should `country` and `is_us` be populated from `location` text at all, given 34/99 empty values and no structured country field?
- Does `tags` element order carry meaning (it is neither alphabetical nor obviously ranked), and are tags assigned by the employer or by RemoteOK?
- Are the department-like tags (`exec`, `medical`, `dev`, `ops`) board taxonomy or employer-supplied attributes, and does mixing them with skills affect how `tags_json` should be queried?
