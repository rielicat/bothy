-- 0009_ui_state.sql (v0.2 UI): state the redesigned screens read instead of
-- working it out on the client.

-- People: the last password sign-in (0 = never). Account: the chat app Bothy
-- messages the person in ('' = the linked one they used last), and whether
-- approval requests stay in Bothy's own Approvals page.
ALTER TABLE users ADD COLUMN last_login_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN preferred_chat TEXT NOT NULL DEFAULT '' CHECK (length(preferred_chat) <= 32);
ALTER TABLE users ADD COLUMN approvals_web_only INTEGER NOT NULL DEFAULT 0 CHECK (approvals_web_only IN (0,1));

-- Activity: refine mode wrote the run's answer.
ALTER TABLE runs ADD COLUMN refined INTEGER NOT NULL DEFAULT 0 CHECK (refined IN (0,1));

-- Releases: the commit the workflow run built and its subject line. The
-- subject is written by whoever can push to the repository, so it is shown
-- as quoted text only, never in cards, notices or prompts.
ALTER TABLE release_deploys ADD COLUMN sha TEXT NOT NULL DEFAULT '';
ALTER TABLE release_deploys ADD COLUMN subject TEXT NOT NULL DEFAULT '' CHECK (length(subject) <= 400);

-- Skills: the files as an admin approved them, so a changed skill's review
-- shows what changed. Kept in the database rather than next to the skill:
-- whoever can edit the skills directory must not be able to edit the copy
-- the review compares against.
CREATE TABLE skill_approved_files (
  skill   TEXT NOT NULL REFERENCES skills(name) ON DELETE CASCADE,
  path    TEXT NOT NULL,
  sha256  TEXT NOT NULL,
  size    INTEGER NOT NULL,
  content TEXT,                                    -- NULL: not text, or over the snapshot caps
  PRIMARY KEY (skill, path)
) STRICT, WITHOUT ROWID;
