# Vendor playbooks

Per-vendor knowledge for the careers-listings flow. The classifier keys a company's careers page to
an applicant-tracking-system (ATS) vendor, and the flow reads the matching playbook to decide *how*
to scrape it: hit a JSON API with code (no browser), or drive a local guided browser-use agent seeded
from the playbook's hints and gotchas.

One JSON file per vendor, committed to git. **The git diff of these files is the review history.**

## Schema

```json
{
  "vendor": "eightfold",
  "verified": true,
  "source": "probe 2026-09-27 (Twilio)",
  "routing": "html_only",
  "careers_pattern": "jobs.<company>.com",
  "api_hint": "search results come from an Eightfold JSON API",
  "hints": { "search_box": ["input[data-testid='position-query-search-search']"] },
  "gotchas": ["the page may render in Spanish locale"]
}
```

- `vendor` — stable key; must match the classifier's vendor name.
- `verified` — `false` means "seed / guess, do not rely on it yet". Only `true` facts are trusted.
- `routing` — `json_api` (fetch with code, no browser), `html_only` (use the guided agent), or
  `unknown` (fall back to the generic guided agent).
- `careers_pattern` — the URL shape a company's careers site takes for this vendor.
- `api_hint` — where the board's own JSON/XML API lives, if known.
- `hints` — **ranked** selector fallbacks (most stable first). Each entry is a CSS selector or a
  `text:<substring>` / `aria-label:<substring>` prefix. Never a single brittle selector.
- `gotchas` — natural-language notes, fed into the agent's task and read by humans.

## Rules

- **Only `verified: true` facts are trusted.** A field flips to verified only after it is observed
  working on a real site.
- **The playbook only changes via a human-reviewed commit.** Run findings land in
  `docs/runs/<date>-*.md`; a human promotes durable lessons into the playbook. No auto-merge.
- **Vendors with no file yet** (workday, icims, successfactors, taleo, oracle) are already rows in
  the `platforms` table; add their playbook after the first probe run against one of their sites.
