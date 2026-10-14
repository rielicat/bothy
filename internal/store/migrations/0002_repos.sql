-- 0002_repos.sql: GitHub connections and their repositories (v0.2 F4). Times:
-- unix ms INTEGER (0 = unset). The PAT or App private key lives in the vault;
-- secret_ref only names it.

CREATE TABLE github_connections (
  id               TEXT PRIMARY KEY,               -- ghc_<26>
  auth             TEXT NOT NULL CHECK (auth IN ('pat','app')),
  account          TEXT NOT NULL DEFAULT '',
  account_type     TEXT NOT NULL DEFAULT '',
  app_id           TEXT NOT NULL DEFAULT '',
  app_slug         TEXT NOT NULL DEFAULT '',
  installation_id  INTEGER NOT NULL DEFAULT 0,
  secret_ref       TEXT NOT NULL CHECK (secret_ref LIKE 'secret:_%'),
  owns_secret      INTEGER NOT NULL DEFAULT 0 CHECK (owns_secret IN (0,1)),
  status           TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','error')),
  error            TEXT NOT NULL DEFAULT '',
  token_expires_at INTEGER NOT NULL DEFAULT 0,
  checked_at       INTEGER NOT NULL DEFAULT 0,
  created_by       TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  CHECK (auth = 'pat' OR (app_id <> '' AND installation_id > 0))
) STRICT;

CREATE TABLE repos (
  owner            TEXT NOT NULL COLLATE NOCASE,
  name             TEXT NOT NULL COLLATE NOCASE,
  connection_id    TEXT NOT NULL REFERENCES github_connections(id) ON DELETE CASCADE,
  github_id        INTEGER NOT NULL DEFAULT 0,
  default_branch   TEXT NOT NULL DEFAULT '',
  private          INTEGER NOT NULL DEFAULT 1 CHECK (private IN (0,1)),
  allowed          INTEGER NOT NULL DEFAULT 0 CHECK (allowed IN (0,1)),
  last_indexed_sha TEXT NOT NULL DEFAULT '',    -- knowledge index (F6)
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  PRIMARY KEY (owner, name),                    -- one connection per repository, whatever the case
  CHECK (owner <> '' AND name <> '')
) STRICT, WITHOUT ROWID;
CREATE INDEX repos_connection ON repos(connection_id);
