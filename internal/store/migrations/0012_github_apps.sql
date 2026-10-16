-- 0012_github_apps.sql: GitHub Apps Bothy created from a manifest (SPEC
-- §14.3). The private key and client secret live in the global vault scope;
-- the row keeps only their references, cleared when the app is retired. A
-- retired row stays until an admin dismisses it, so Settings can say where
-- to delete the app on GitHub. Bothy never deletes apps on GitHub. Times:
-- unix ms. A later rebuild of github_connections must rebuild this table too
-- (its foreign key), as 0008 did for repos.

CREATE TABLE github_apps (
  id                TEXT PRIMARY KEY,                                  -- gha_<26>
  app_id            INTEGER NOT NULL UNIQUE CHECK (app_id > 0),        -- GitHub's id
  slug              TEXT NOT NULL CHECK (length(slug) BETWEEN 1 AND 100),
  name              TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  owner             TEXT NOT NULL CHECK (length(owner) BETWEEN 1 AND 39),
  owner_type        TEXT NOT NULL CHECK (owner_type IN ('Organization','User')),
  html_url          TEXT NOT NULL CHECK (html_url LIKE 'https://_%' OR html_url LIKE 'http://_%'), -- GitHub's: /apps/<slug>, /github-apps/<slug> on GHES
  client_id         TEXT NOT NULL DEFAULT '',
  key_ref           TEXT NOT NULL DEFAULT '' CHECK (key_ref = '' OR key_ref LIKE 'secret:_%'),
  client_secret_ref TEXT NOT NULL DEFAULT '' CHECK (client_secret_ref = '' OR client_secret_ref LIKE 'secret:_%'),
  connection_id     TEXT UNIQUE REFERENCES github_connections(id) ON DELETE SET NULL,
  replaces          TEXT NOT NULL DEFAULT '',                          -- connection to swap out once installed
  retired_at        INTEGER NOT NULL DEFAULT 0,
  created_by        TEXT NOT NULL DEFAULT '',
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  CHECK (retired_at > 0 OR key_ref <> ''),
  CHECK (retired_at = 0 OR (connection_id IS NULL AND key_ref = '' AND client_secret_ref = '' AND replaces = ''))
) STRICT;
