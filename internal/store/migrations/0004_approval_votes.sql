-- 0004_approval_votes.sql (v0.2 F5): votes of multi-approver approvals and the
-- deploy records of release_deploy. Times: unix ms INTEGER (0 = unset).

CREATE TABLE approval_votes (                    -- no FK to users: a purged voter's vote stays on record
  approval_id TEXT NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
  user_id     TEXT NOT NULL,
  choice      TEXT NOT NULL CHECK (choice IN ('allow_once','deny')),
  via         TEXT NOT NULL,
  at          INTEGER NOT NULL,
  PRIMARY KEY (approval_id, user_id)             -- one vote per approver
) STRICT, WITHOUT ROWID;

CREATE TABLE release_deploys (
  id              TEXT PRIMARY KEY,
  environment     TEXT NOT NULL,
  repo            TEXT NOT NULL,
  workflow        TEXT NOT NULL,
  ref             TEXT NOT NULL,
  principal_id    TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  session_id      TEXT NOT NULL,
  run_id          TEXT NOT NULL DEFAULT '',
  approval_id     TEXT NOT NULL DEFAULT '',
  requested_by    TEXT NOT NULL,
  approved_by     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(approved_by) AND json_type(approved_by) = 'array'),
  chat            TEXT CHECK (chat IS NULL OR json_valid(chat)),
  status          TEXT NOT NULL CHECK (status IN ('dispatched','running','succeeded','failed','unknown')),
  workflow_run_id INTEGER NOT NULL DEFAULT 0,
  run_url         TEXT NOT NULL DEFAULT '',
  conclusion      TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  finished_at     INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX release_deploys_recent ON release_deploys(created_at);
CREATE INDEX release_deploys_open ON release_deploys(created_at) WHERE status IN ('dispatched','running');
