-- 0011_sign_ins.sql: the Google and GitHub accounts people sign in with
-- (SPEC §15). Only who the provider says the person is; no token is ever
-- stored.

CREATE TABLE sign_ins (
  provider     TEXT NOT NULL CHECK (provider IN ('google','github')),
  subject      TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),  -- Google "sub", GitHub numeric user id
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email        TEXT NOT NULL DEFAULT '' CHECK (length(email) <= 320),    -- verified; GitHub: the address that matched the sign-in list, else the primary one, or ''
  login        TEXT NOT NULL DEFAULT '' CHECK (length(login) <= 64),     -- GitHub login; '' for Google
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (provider, subject)
) STRICT, WITHOUT ROWID;
-- One link per provider per person.
CREATE UNIQUE INDEX sign_ins_user_provider ON sign_ins(user_id, provider);
