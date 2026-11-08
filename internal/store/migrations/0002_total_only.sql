-- Records from sources that report only a combined token count (newer Codex
-- CLI logs). total_only marks them; total_only_tokens holds the count while
-- the input/output/cache columns stay 0. Existing rows keep both at 0.
ALTER TABLE usage_records ADD COLUMN total_only        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN total_only_tokens INTEGER NOT NULL DEFAULT 0;

-- Parser change: builds before this one skipped Codex entries that carry only
-- total_tokens and stored unnamed models as 'unknown', yet advanced the read
-- offset past those lines. Forget the Codex read positions so every rollout is
-- read again once. Record identity is unchanged (see docs/design.md), so the re-read adds
-- the missing entries, corrects the model of existing rows and never counts a
-- response twice. Records of rollouts that are gone from disk stay.
DELETE FROM ingest_state WHERE source = 'codex_cli';
