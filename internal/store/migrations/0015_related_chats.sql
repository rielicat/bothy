-- 0015_related_chats.sql: chats that came from another chat or a schedule,
-- titles Bothy gave that the first message replaces, quiet check-ins kept
-- out of the list, and the one-time welcome chat. Times: unix ms.

-- The chat a chat continued from, and the top of its family (NULL = none).
-- Both are fixed when the chat is made. ADD COLUMN ... REFERENCES is allowed
-- because the default is NULL.
ALTER TABLE sessions ADD COLUMN parent_id   TEXT REFERENCES sessions(id) ON DELETE SET NULL;
ALTER TABLE sessions ADD COLUMN root_id     TEXT REFERENCES sessions(id) ON DELETE SET NULL;
-- The schedule whose result this chat holds. No FK: a deleted schedule's
-- results keep their group and say "From a deleted schedule".
ALTER TABLE sessions ADD COLUMN schedule_id TEXT NOT NULL DEFAULT '';
-- 1 = Bothy chose the title ("X (clean)", "Welcome to Bothy"); the first
-- message's draft replaces it, then the generated title.
ALTER TABLE sessions ADD COLUMN title_auto  INTEGER NOT NULL DEFAULT 0 CHECK (title_auto IN (0,1));
CREATE INDEX sessions_parent ON sessions(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX sessions_root   ON sessions(root_id)   WHERE root_id   IS NOT NULL;

-- Hidden quiet check-ins age out like hidden API one-shots (store/retention.go).
DROP INDEX sessions_hidden_api;
CREATE INDEX sessions_hidden_due ON sessions(max(created_at, updated_at, last_message_at))
  WHERE hidden = 1 AND origin IN ('api','schedule') AND archived = 0;

-- Results of schedules that make a fresh chat per run (not a schedule's own target chat).
UPDATE sessions SET schedule_id = COALESCE((SELECT r.schedule_id FROM runs r
    WHERE r.session_id = sessions.id AND r.schedule_id IS NOT NULL ORDER BY r.created_at LIMIT 1), '')
  WHERE origin = 'schedule' AND id NOT IN (SELECT session_id FROM schedules WHERE session_id IS NOT NULL);

-- Past all-clear check-ins stop being listed. Hidden only; retention archives them later,
-- so one someone wrote in (a message run) stays. Approvals are reached through their runs:
-- approvals has no index on session_id, and a scan per check-in would stall the upgrade.
UPDATE sessions SET hidden = 1 WHERE origin = 'schedule' AND hidden = 0
  AND EXISTS (SELECT 1 FROM runs r WHERE r.session_id = sessions.id AND r.trigger = 'heartbeat' AND r.status = 'done')
  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.session_id = sessions.id AND r.trigger = 'message')
  AND NOT EXISTS (SELECT 1 FROM runs r JOIN approvals a ON a.run_id = r.id WHERE r.session_id = sessions.id)
  AND (SELECT ltrim(m.text, ' *_`>') FROM messages m WHERE m.session_id = sessions.id AND m.role = 'assistant'
       AND m.kind = 'normal' AND m.text != '' ORDER BY m.seq DESC LIMIT 1) LIKE 'HEARTBEAT_OK%';

-- Human dates and the check-in's name in result titles.
UPDATE sessions SET title = 'Check-in' || substr(title, 10)
  WHERE schedule_id != '' AND title LIKE 'heartbeat · %';
UPDATE sessions SET title = substr(title, 1, length(title) - 10)
    || CAST(CAST(substr(title, -2) AS INTEGER) AS TEXT) || ' '
    || CASE substr(title, -5, 2) WHEN '01' THEN 'Jan' WHEN '02' THEN 'Feb' WHEN '03' THEN 'Mar' WHEN '04' THEN 'Apr'
         WHEN '05' THEN 'May' WHEN '06' THEN 'Jun' WHEN '07' THEN 'Jul' WHEN '08' THEN 'Aug' WHEN '09' THEN 'Sep'
         WHEN '10' THEN 'Oct' WHEN '11' THEN 'Nov' ELSE 'Dec' END
  WHERE schedule_id != '' AND title GLOB '* · [0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]';

-- The welcome chat is made once per web account; existing accounts never get
-- one (max: 0 would read as "not welcomed yet").
ALTER TABLE users ADD COLUMN welcomed_at INTEGER NOT NULL DEFAULT 0;
UPDATE users SET welcomed_at = max(created_at, 1);
