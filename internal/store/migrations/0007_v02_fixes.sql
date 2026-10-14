-- 0007_v02_fixes.sql: state kept with what it belongs to.

-- The follower reads a run's status through the dispatcher that started it,
-- even after the repository joined or left a connection's allowlist; '' is a
-- deploy recorded before this column, followed through today's choice.
ALTER TABLE release_deploys ADD COLUMN via TEXT NOT NULL DEFAULT '' CHECK (via IN ('', 'connection', 'token'));

-- Indexes are per principal, so the commit a repository was last indexed at
-- is too: knowledge_repos.sha is the latest requested commit, indexed_sha the
-- one the latest finished index read. The shared repos.last_indexed_sha told
-- every user what someone else had indexed.
ALTER TABLE knowledge_repos ADD COLUMN indexed_sha TEXT NOT NULL DEFAULT '';
UPDATE knowledge_repos SET indexed_sha = sha WHERE status = 'ready';
ALTER TABLE repos DROP COLUMN last_indexed_sha;
