CREATE TABLE events (
  global_seq     INTEGER PRIMARY KEY AUTOINCREMENT,
  stream_id      TEXT    NOT NULL,
  sequence       INTEGER NOT NULL,          -- per-stream, gapless, from 1
  event_type     TEXT    NOT NULL,
  event_version  INTEGER NOT NULL DEFAULT 1,
  occurred_at    TEXT    NOT NULL,          -- RFC3339 UTC, fixed width
  recorded_at    TEXT    NOT NULL,
  actor          TEXT    NOT NULL,
  causation_seq  INTEGER,
  correlation_id TEXT    NOT NULL,
  payload        TEXT    NOT NULL,
  UNIQUE (stream_id, sequence),              -- the concurrency control
  CHECK (json_valid(payload)),
  CHECK (length(payload) <= 65536)           -- 64 KiB. Anything larger is an artifact.
) STRICT;

CREATE INDEX events_type_time   ON events (event_type, global_seq DESC);
CREATE INDEX events_correlation ON events (correlation_id, global_seq);
