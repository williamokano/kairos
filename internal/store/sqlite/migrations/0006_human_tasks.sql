-- L20-webui.md's Documented decision #5: the home page's "waiting on
-- you" section did an O(active runs) scan (GetRun on every non-terminal
-- run, looking for an Executing==waiting NodeExecution) because no real
-- index existed. One row per currently-open decision — a wait: human
-- task OR a parked confirm-tier effect, both surfaced identically since
-- both resolve through kairos approve (see engine.Approve).
CREATE TABLE human_task_index (
  run_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  kind TEXT NOT NULL,       -- "human" | "effect_confirm"
  opened_at TEXT NOT NULL,
  PRIMARY KEY (run_id, node_id)
) STRICT;
