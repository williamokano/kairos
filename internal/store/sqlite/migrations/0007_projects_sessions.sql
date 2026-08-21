-- internal/project: a Project names a real working directory (optionally
-- git-backed); a Session is a stable, resumable chat identity bound to
-- one Project (or none). Same posture as 0003_triggers.sql's source/
-- source_cursor tables: plain, daemon-owned, durable rows, never folded
-- through domain.Advance (these aren't run-scoped facts).
CREATE TABLE project (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, repo_path TEXT NOT NULL,
  git_backed INTEGER NOT NULL DEFAULT 0,
  created_by TEXT, created_at TEXT NOT NULL
) STRICT;

CREATE TABLE session (
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES project(id) ON DELETE SET NULL,
  actor TEXT NOT NULL,
  work_dir TEXT NOT NULL,
  branch TEXT,
  native_session_id TEXT,
  conversation_run_id TEXT,
  last_run_id TEXT,
  run_count INTEGER NOT NULL DEFAULT 0,
  created_by TEXT, created_at TEXT NOT NULL, last_used_at TEXT NOT NULL
) STRICT;

CREATE INDEX session_project ON session (project_id);
