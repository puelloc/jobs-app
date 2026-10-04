-- 018_company_listings_url.sql: remember the filtered listings URL a scrape resolved for a company.
--
-- The per-company scrape spends nearly all of its wall-clock inside the browser-use agent, which asks
-- a local model to walk from the careers page to the filtered listings URL and to judge whether
-- remote software-engineering roles exist there. That answer is stable from one sweep to the next, but
-- it was never written down, so every sweep re-derived the same URL for every company one slow agent
-- run at a time.
--
-- Storing it lets a re-sweep go straight to the fetch. The agent runs again only when the cache is
-- older than the configured TTL, or when a caller explicitly asks to re-resolve.
--
-- NULL means "never resolved", which is deliberately different from "resolved to the empty string":
-- existing rows start cold and the first scrape of each company fills it in.
ALTER TABLE companies ADD COLUMN listings_url TEXT;

-- When the URL above was resolved. Separate from career_site_url_checked_at, which moves for a
-- different reason: validating the careers page, not finding its listings page.
ALTER TABLE companies ADD COLUMN listings_url_resolved_at TEXT;
