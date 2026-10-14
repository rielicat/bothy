-- 0005_v02_fixes.sql: state kept with what it belongs to.

-- The follower reads a run's status through the dispatcher that started it,
-- even after the repository joined or left a connection's allowlist; '' is a
-- deploy recorded before this column, followed through today's choice.
ALTER TABLE release_deploys ADD COLUMN via TEXT NOT NULL DEFAULT '' CHECK (via IN ('', 'connection', 'token'));
