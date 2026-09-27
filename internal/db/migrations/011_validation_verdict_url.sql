-- 011_validation_verdict_url.sql: record which URL a validation verdict was reached about.
--
-- Why the verdict needs the URL beside it: the skip-validated rule ("do not re-fetch what has an
-- answer") is only sound while the answer is about the URL that is stored *now*. The resolution
-- ladder keeps running between validation passes - a live resolve run was observed adding 115 URLs
-- to the same table while a validation sweep was in flight - and when it changes a company's
-- career_site_url the old verdict silently becomes a statement about a URL nobody stores any more.
-- The company would then be skipped forever, carrying a verdict about a page it no longer points
-- at. This is the "check that silently passes when its input is absent" family in the plan's
-- recurring failure modes: the guard has to compare against the thing it is judging.
--
-- Nullable, and NULL means "unknown, so do not trust this verdict for skipping". Rows written before
-- this migration are backfilled from their own attempt rows below, which is exactly the URL the
-- verdict was reached about.
ALTER TABLE companies ADD COLUMN career_site_url_verdict_url TEXT;

-- Backfill from the attempt that produced the verdict. The attempt index orders a company's
-- candidates, and the first browser_use attempt is always the stored URL itself; the agent's
-- attempt, when there is one, is a different URL and must not be mistaken for what was judged.
UPDATE companies
   SET career_site_url_verdict_url = (
        SELECT a.candidate_url
          FROM url_resolution_attempts a
         WHERE a.company_id = companies.id
           AND a.run_id = companies.career_site_url_verdict_run_id
           AND a.source = 'browser_use'
         ORDER BY a.attempt_index
         LIMIT 1)
 WHERE career_site_url_verdict IS NOT NULL
   AND career_site_url_verdict_run_id IS NOT NULL;
