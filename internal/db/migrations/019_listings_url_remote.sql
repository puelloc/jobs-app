-- 019_listings_url_remote.sql: record whether the agent found remote roles at the cached listings URL.
--
-- Resolving a listings URL costs one slow local-model agent run, so migration 018 began caching the URL
-- and reusing it. The verdict the same agent reached in the same run - "are there remote
-- software-engineering roles open here" - was not cached, so a company the agent found nothing for
-- re-ran the agent on every sweep. That is most companies, which is why the cache was paying off for
-- only a minority.
--
-- Caching the verdict lets a re-sweep skip the agent for those companies too. It is also what keeps the
-- fetch safe: cmd/scrape stores a posting as remote on the strength of the URL being a remote-filtered
-- search URL, and that is only known to hold when the agent confirmed remote roles there. Where it did
-- not, the fetch falls back to the posting's own structured remote signal instead of assuming.
--
-- NULL means no agent has resolved this company; 1 means it confirmed remote roles at the cached URL;
-- 0 means it found none.
ALTER TABLE companies ADD COLUMN listings_url_remote_confirmed INTEGER
    CHECK (listings_url_remote_confirmed IS NULL OR listings_url_remote_confirmed IN (0, 1));

-- Every URL cached before this migration came from the code path that only cached on confirmation -
-- cmd/scrape returned before caching when the agent reported no remote roles - so 1 is the accurate
-- reading of the rows that already exist.
UPDATE companies
   SET listings_url_remote_confirmed = 1
 WHERE listings_url IS NOT NULL;
