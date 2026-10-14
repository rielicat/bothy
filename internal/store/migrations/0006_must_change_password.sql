-- 0006_must_change_password.sql (v0.2 UI): an account an admin created or
-- reset carries a one-time password until its owner chooses their own.

ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0,1));
