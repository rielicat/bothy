-- 0008_external_secret_refs.sql: a GitHub connection's secret_ref may name a
-- secret in AWS Secrets Manager ("aws-sm:<secret>") as well as the vault.
--
-- SQLite cannot change a CHECK in place, so github_connections is rebuilt.
-- Migrations run in a transaction with foreign keys on, where dropping the
-- old table would cascade into repos; repos is therefore rebuilt first
-- against the new table, and renaming the new table rewrites repos'
-- REFERENCES to the final name.

CREATE TABLE github_connections_new (
  id               TEXT PRIMARY KEY,               -- ghc_<26>
  auth             TEXT NOT NULL CHECK (auth IN ('pat','app')),
  account          TEXT NOT NULL DEFAULT '',
  account_type     TEXT NOT NULL DEFAULT '',
  app_id           TEXT NOT NULL DEFAULT '',
  app_slug         TEXT NOT NULL DEFAULT '',
  installation_id  INTEGER NOT NULL DEFAULT 0,
  secret_ref       TEXT NOT NULL CHECK (secret_ref LIKE 'secret:_%' OR secret_ref LIKE 'aws-sm:_%'),
  owns_secret      INTEGER NOT NULL DEFAULT 0 CHECK (owns_secret IN (0,1)),
  status           TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','error')),
  error            TEXT NOT NULL DEFAULT '',
  token_expires_at INTEGER NOT NULL DEFAULT 0,
  checked_at       INTEGER NOT NULL DEFAULT 0,
  created_by       TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  CHECK (auth = 'pat' OR (app_id <> '' AND installation_id > 0)),
  CHECK (owns_secret = 0 OR secret_ref LIKE 'secret:_%')
) STRICT;

INSERT INTO github_connections_new (id, auth, account, account_type, app_id, app_slug, installation_id, secret_ref, owns_secret,
  status, error, token_expires_at, checked_at, created_by, created_at, updated_at)
SELECT id, auth, account, account_type, app_id, app_slug, installation_id, secret_ref, owns_secret,
  status, error, token_expires_at, checked_at, created_by, created_at, updated_at
FROM github_connections;

CREATE TABLE repos_new (
  owner            TEXT NOT NULL COLLATE NOCASE,
  name             TEXT NOT NULL COLLATE NOCASE,
  connection_id    TEXT NOT NULL REFERENCES github_connections_new(id) ON DELETE CASCADE,
  github_id        INTEGER NOT NULL DEFAULT 0,
  default_branch   TEXT NOT NULL DEFAULT '',
  private          INTEGER NOT NULL DEFAULT 1 CHECK (private IN (0,1)),
  allowed          INTEGER NOT NULL DEFAULT 0 CHECK (allowed IN (0,1)),
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  PRIMARY KEY (owner, name),                    -- one connection per repository, whatever the case
  CHECK (owner <> '' AND name <> '')
) STRICT, WITHOUT ROWID;

INSERT INTO repos_new (owner, name, connection_id, github_id, default_branch, private, allowed, created_at, updated_at)
SELECT owner, name, connection_id, github_id, default_branch, private, allowed, created_at, updated_at
FROM repos;

DROP TABLE repos;
DROP TABLE github_connections;
ALTER TABLE github_connections_new RENAME TO github_connections;
ALTER TABLE repos_new RENAME TO repos;
CREATE INDEX repos_connection ON repos(connection_id);
