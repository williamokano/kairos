CREATE TABLE projection_offsets (
  name            TEXT    PRIMARY KEY,
  version         INTEGER NOT NULL,
  last_global_seq INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE run_state_projection (
  run_id     TEXT NOT NULL PRIMARY KEY,
  state_json TEXT NOT NULL,
  status     TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (json_valid(state_json))
) STRICT;

CREATE TABLE run_index (
  run_id     TEXT NOT NULL PRIMARY KEY,
  status     TEXT NOT NULL,
  started_at TEXT,
  updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX run_index_status ON run_index (status, updated_at DESC);
