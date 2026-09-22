# jobs-app

Scrapes remote software-engineering jobs into SQLite. Start with
`docs/scraper-design.md` for run behavior and `docs/remoteok-mapping.md` for the
field mapping.

## Known issues

- **Mojibake in scraped text.** RemoteOK's payload carries UTF-8 bytes that read
  as Latin-1 for common punctuation, so descriptions, positions, and some
  locations are stored with artefacts such as `Iâm` where the source intends
  `I’m`. This affects 78 of 99 descriptions in the 2026-09-22 capture
  (`data/raw/remoteok-2026-09-22.json`). Text is stored exactly as received and
  is **not** repaired: cleanup is a separate pass, and repairing at write time
  would make the stored value disagree with the raw capture kept beside it.
