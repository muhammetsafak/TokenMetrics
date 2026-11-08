-- Total-only rows have no meaning without their columns (all other counters
-- are 0), so they are removed instead of being left as empty records. The
-- Codex read positions are cleared so an older build reads the rollouts again.
DELETE FROM usage_records WHERE total_only = 1;
DELETE FROM ingest_state WHERE source = 'codex_cli';
ALTER TABLE usage_records DROP COLUMN total_only_tokens;
ALTER TABLE usage_records DROP COLUMN total_only;
