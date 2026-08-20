-- 08-triggers.md: source config, cursors, and trigger dedupe are all owned
-- by the daemon, never by a plugin (a plugin that keeps its own cursor
-- cannot be restarted safely).
CREATE TABLE source (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, config TEXT NOT NULL,
  flow TEXT, project TEXT,
  interval_s INTEGER NOT NULL DEFAULT 120,
  enabled INTEGER NOT NULL DEFAULT 1,
  health TEXT NOT NULL DEFAULT 'unknown',
  health_reason TEXT, consecutive_errors INTEGER NOT NULL DEFAULT 0,
  last_poll_at TEXT, next_poll_at TEXT
) STRICT;

CREATE TABLE source_cursor (
  source_id TEXT PRIMARY KEY REFERENCES source(id) ON DELETE CASCADE,
  cursor TEXT, etag TEXT, updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE trigger_dedupe (
  dedupe_key TEXT PRIMARY KEY,
  source_id TEXT, item_id TEXT, run_id TEXT,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL
) STRICT;

CREATE INDEX trigger_dedupe_expires ON trigger_dedupe (expires_at);
