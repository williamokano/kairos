-- NL-49 (11-limitations.md): POST /runs's Idempotency-Key/form nonce was
-- rendered and wired through the web composer but never read or
-- deduplicated server-side, so a genuine double-submit (double-click, a
-- retried request after a dropped response) created two runs instead of
-- one. Independent of L16's trigger_dedupe table (that one dedupes
-- trigger-created runs by source cursor, a different identity — this one
-- dedupes direct POST /runs calls by a client-supplied key).
CREATE TABLE run_idempotency (
  idempotency_key TEXT PRIMARY KEY,
  run_id TEXT,
  created_at TEXT NOT NULL
) STRICT;
