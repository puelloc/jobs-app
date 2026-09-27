-- 013_career_listings_platform.sql: the platform row a browser-use listings scrape is recorded against.
--
-- 23 is career_resolution and 24 is career_validation. A listings scrape answers a third question -
-- "what remote-US software-engineering roles are open right now" - and its scrape_runs row must be
-- distinguishable from both in the /api/runs dashboard.
INSERT INTO platforms (id, name, platform_type) VALUES (25, 'career_listings', 'reference');
