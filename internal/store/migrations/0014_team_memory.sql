-- 0014_team_memory.sql: the history of TEAM.md, what Bothy remembers for
-- everyone (<data>/memory/team/TEAM.md). It belongs to no principal, so it
-- lives outside memory_events (whose rows go with their principal) and a
-- user purge never touches it. approval_id is the admin's approval an agent
-- change ran under; '' for an admin's own edit on the web. Times: unix ms.

CREATE TABLE team_memory_events (
  id          INTEGER PRIMARY KEY,
  op          TEXT NOT NULL CHECK (op IN ('add','replace','remove','edit')),
  before      TEXT NOT NULL DEFAULT '',
  after       TEXT NOT NULL DEFAULT '',
  run_id      TEXT NOT NULL DEFAULT '',
  approval_id TEXT NOT NULL DEFAULT '',
  actor       TEXT NOT NULL,                     -- agent:<run> | user:<id>
  provenance  TEXT NOT NULL DEFAULT 'user',
  created_at  INTEGER NOT NULL
) STRICT;
