-- 003_fix_remoteok_endpoint.sql: fixes the remoteok platform URL, which pointed at the HTML listing page instead of the JSON feed.
UPDATE platforms SET base_url_pattern = 'https://remoteok.com/api' WHERE id = 1;
