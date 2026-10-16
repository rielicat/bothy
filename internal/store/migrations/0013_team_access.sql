-- 0013_team_access.sql: people in chat apps stop linking to Bothy accounts.
-- Linked and guest identities become teammates with a person space of
-- their own; group chats (channels) lose their owner; link and
-- approve-sender codes go. Rebuilt tables: principals (user_id NULL: owned
-- by the team), spaces, channel_identities. Like every migration this runs
-- with foreign keys off: the migrator fails it on any foreign key violation
-- that wasn't there before, and m_rows fails it if a rebuild lost rows.

CREATE TEMP TABLE m_rows (
  name   TEXT PRIMARY KEY,
  before INTEGER NOT NULL,
  after  INTEGER CHECK (after IS NULL OR after = before)
);
INSERT INTO m_rows(name, before)
SELECT 'sessions', count(*) FROM sessions UNION ALL
SELECT 'messages', count(*) FROM messages UNION ALL
SELECT 'runs', count(*) FROM runs UNION ALL
SELECT 'approvals', count(*) FROM approvals UNION ALL
SELECT 'grants', count(*) FROM grants UNION ALL
SELECT 'schedules', count(*) FROM schedules UNION ALL
SELECT 'memory_events', count(*) FROM memory_events UNION ALL
SELECT 'uploads', count(*) FROM uploads UNION ALL
SELECT 'knowledge_repos', count(*) FROM knowledge_repos UNION ALL
SELECT 'release_deploys', count(*) FROM release_deploys UNION ALL
SELECT 'llm_calls', count(*) FROM llm_calls UNION ALL
SELECT 'principals', count(*) FROM principals UNION ALL
SELECT 'spaces', count(*) FROM spaces UNION ALL
SELECT 'channel_identities', count(*) FROM channel_identities;

-- A linked identity whose account is enabled gets a person space. One it
-- had as a guest before it was linked (same chat) is reused.
CREATE TEMP TABLE m_link AS
SELECT ci.channel, ci.external_id,
       COALESCE(NULLIF(ci.display_name, ''), NULLIF(ci.username, ''), ci.external_id) AS title,
       u.role IN ('owner', 'admin') AS approver,
       u.disabled AS off,
       (SELECT s.principal_id FROM spaces s
         WHERE s.kind = 'guest' AND s.channel = ci.channel AND s.chat_id IN (ci.external_id, ci.dm_chat_id)
         ORDER BY s.created_at LIMIT 1) AS reuse,
       'spc_' || lower(hex(randomblob(13))) AS fresh,
       'ws_' || lower(hex(randomblob(13))) AS ws
  FROM channel_identities ci JOIN users u ON u.id = ci.user_id
 WHERE ci.status = 'linked';
UPDATE m_rows SET before = before + (SELECT count(*) FROM m_link WHERE reuse IS NULL AND off = 0)
 WHERE name IN ('principals', 'spaces');

-- What changed, for the admins' banner and the audit row written at boot;
-- nothing is reported when nothing moved (a new install among others).
CREATE TEMP TABLE m_report AS
SELECT (SELECT count(*) FROM m_link WHERE off = 0) AS linked,
       (SELECT count(*) FROM m_link WHERE off = 0 AND approver) AS approvers,
       (SELECT count(*) FROM m_link WHERE off = 1) AS disabled_accounts,
       (SELECT count(*) FROM channel_identities ci JOIN spaces s ON s.principal_id = ci.principal_id
         WHERE ci.status = 'guest' AND s.enabled = 1) AS guests,
       (SELECT count(*) FROM channel_identities ci JOIN spaces s ON s.principal_id = ci.principal_id
         WHERE ci.status = 'guest' AND s.enabled = 1 AND ci.channel = 'telegram') AS telegram_guests,
       (SELECT count(*) FROM channel_identities ci LEFT JOIN spaces s ON s.principal_id = ci.principal_id
         WHERE ci.status = 'guest' AND COALESCE(s.enabled, 0) = 0) AS guests_blocked,
       (SELECT count(*) FROM schedules sc JOIN spaces s ON s.principal_id = sc.principal_id
         WHERE s.kind = 'group' AND sc.enabled = 1
           AND NOT (sc.deliver IS NOT NULL AND json_extract(sc.deliver, '$.channel') = s.channel
                    AND json_extract(sc.deliver, '$.chat_id') = s.chat_id)) AS schedules_paused;

CREATE TABLE principals_new (
  id           TEXT PRIMARY KEY,               -- user principals: same string as users.id
  kind         TEXT NOT NULL CHECK (kind IN ('user','space','person')),
  user_id      TEXT REFERENCES users(id) ON DELETE CASCADE,  -- users only; NULL = owned by the team (channels, teammates)
  display_name TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL UNIQUE,
  tool_profile TEXT NOT NULL CHECK (tool_profile IN ('standard','restricted')),
  created_at   INTEGER NOT NULL,
  CHECK ((kind = 'user') = (user_id IS NOT NULL)),
  CHECK (kind <> 'user' OR id = user_id),
  CHECK (kind <> 'person' OR tool_profile = 'standard')
) STRICT;
INSERT INTO principals_new(id, kind, user_id, display_name, workspace_id, tool_profile, created_at)
SELECT p.id,
       CASE WHEN p.kind = 'user' THEN 'user' WHEN s.kind = 'guest' THEN 'person' ELSE 'space' END,
       CASE WHEN p.kind = 'user' THEN p.user_id END,
       p.display_name, p.workspace_id,
       CASE WHEN s.kind = 'guest' THEN 'standard' ELSE p.tool_profile END,
       p.created_at
  FROM principals p LEFT JOIN spaces s ON s.principal_id = p.id;
INSERT INTO principals_new(id, kind, user_id, display_name, workspace_id, tool_profile, created_at)
SELECT fresh, 'person', NULL, title, ws, 'standard', CAST(unixepoch('subsec') * 1000 AS INTEGER)
  FROM m_link WHERE reuse IS NULL AND off = 0;

CREATE TABLE spaces_new (
  principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('group','person')),
  channel      TEXT NOT NULL,
  chat_id      TEXT NOT NULL,                  -- person: the teammate's external id or DM (DMs route by identity, never by this)
  chat_kind    TEXT NOT NULL CHECK (chat_kind IN ('dm','group')),
  team_id      TEXT NOT NULL DEFAULT '',       -- group: Discord guild id; '' when unknown
  team_name    TEXT NOT NULL DEFAULT '',       -- group: the server or workspace name, for admins
  added_by     TEXT NOT NULL DEFAULT '',       -- group: platform id of whoever added Bothy, when reported
  title        TEXT NOT NULL DEFAULT '',
  members      INTEGER NOT NULL DEFAULT 0,     -- group: people in the chat or server as last reported; 0 = unknown
  external     INTEGER NOT NULL DEFAULT 0 CHECK (external IN (0,1)),  -- group: Slack says people from other companies are in it
  enabled      INTEGER NOT NULL DEFAULT 0,     -- person: 0 while the teammate is blocked
  mention_only INTEGER NOT NULL DEFAULT 1,
  reviewed_at  INTEGER NOT NULL DEFAULT 0,     -- group: an admin turned it on or ignored it; 0 = still in Approvals
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  UNIQUE (channel, chat_id)
) STRICT;
INSERT INTO spaces_new(principal_id, kind, channel, chat_id, chat_kind, title, enabled, mention_only, reviewed_at,
    created_at, updated_at)
SELECT principal_id, CASE kind WHEN 'guest' THEN 'person' ELSE 'group' END, channel, chat_id, chat_kind, title,
       enabled, mention_only,
       CASE WHEN kind = 'group' AND enabled = 0 AND updated_at = created_at THEN 0 ELSE updated_at END,
       created_at, updated_at
  FROM spaces;
INSERT INTO spaces_new(principal_id, kind, channel, chat_id, chat_kind, title, enabled, mention_only, reviewed_at,
    created_at, updated_at)
SELECT fresh, 'person', channel, external_id, 'dm', title, 1, 0,
       CAST(unixepoch('subsec') * 1000 AS INTEGER), CAST(unixepoch('subsec') * 1000 AS INTEGER),
       CAST(unixepoch('subsec') * 1000 AS INTEGER)
  FROM m_link WHERE reuse IS NULL AND off = 0;

CREATE TABLE channel_identities_new (
  channel        TEXT NOT NULL,
  external_id    TEXT NOT NULL,
  username       TEXT NOT NULL DEFAULT '',
  display_name   TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL CHECK (status IN ('pending','teammate','blocked')),
  principal_id   TEXT REFERENCES principals(id) ON DELETE SET NULL,
  admitted_via   TEXT NOT NULL DEFAULT '' CHECK (admitted_via IN ('','membership','admin')),
  can_approve    INTEGER NOT NULL DEFAULT 0 CHECK (can_approve IN (0,1)),
  membership     TEXT NOT NULL DEFAULT '' CHECK (membership IN ('','unknown','member','outsider')),
  membership_at  INTEGER NOT NULL DEFAULT 0,
  dm_chat_id     TEXT NOT NULL DEFAULT '',
  first_seen_at  INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  last_notice_at INTEGER NOT NULL DEFAULT 0,
  greet          TEXT NOT NULL DEFAULT 'hello' CHECK (greet IN ('','hello','moved')),
  first_text     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (channel, external_id),
  CHECK (status <> 'teammate' OR (principal_id IS NOT NULL AND admitted_via <> '')),
  CHECK (can_approve = 0 OR status = 'teammate')
) STRICT, WITHOUT ROWID;
INSERT INTO channel_identities_new(channel, external_id, username, display_name, status, principal_id, admitted_via,
    can_approve, membership, membership_at, dm_chat_id, first_seen_at, last_seen_at, last_notice_at, greet, first_text)
SELECT channel, external_id, username, display_name, new_status,
       CASE WHEN old_status = 'linked' AND off = 0 THEN COALESCE(reuse, fresh)
            WHEN old_status = 'linked' THEN reuse    -- blocked, but Delete their data still reaches the guest space
            ELSE principal_id END,
       CASE WHEN new_status = 'teammate' THEN 'admin' ELSE '' END,
       CASE WHEN new_status = 'teammate' AND old_status = 'linked' THEN approver ELSE 0 END,
       CASE WHEN channel = 'telegram' THEN 'unknown' ELSE '' END,
       0, dm_chat_id, first_seen_at, last_seen_at, last_notice_at,
       CASE WHEN new_status = 'teammate' AND old_status = 'linked' THEN 'moved'
            WHEN new_status = 'teammate' THEN ''
            ELSE 'hello' END,
       CASE WHEN old_status = 'pending' THEN first_text ELSE '' END
  FROM (SELECT ci.channel, ci.external_id, ci.username, ci.display_name, ci.status AS old_status, ci.principal_id,
               ci.dm_chat_id, ci.first_seen_at, ci.last_seen_at, ci.last_notice_at, ci.first_text,
               m.off, m.reuse, m.fresh, m.approver,
               CASE WHEN ci.status = 'guest' AND gs.enabled = 1 THEN 'teammate'
                    WHEN ci.status = 'guest' THEN 'blocked'      -- an admin had turned their guest space off
                    WHEN ci.status = 'linked' AND m.off = 0 THEN 'teammate'
                    WHEN ci.status = 'linked' THEN 'blocked'     -- the account is disabled
                    ELSE ci.status END AS new_status
          FROM channel_identities ci
          LEFT JOIN m_link m ON m.channel = ci.channel AND m.external_id = ci.external_id
          LEFT JOIN spaces gs ON gs.principal_id = ci.principal_id AND gs.kind = 'guest');

DROP TABLE channel_identities;
DROP TABLE spaces;
DROP TABLE principals;
ALTER TABLE principals_new RENAME TO principals;
ALTER TABLE spaces_new RENAME TO spaces;
ALTER TABLE channel_identities_new RENAME TO channel_identities;
CREATE INDEX principals_user ON principals(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX channel_identities_principal ON channel_identities(principal_id) WHERE principal_id IS NOT NULL;
CREATE INDEX channel_identities_status ON channel_identities(status);

-- A person space is on exactly while its teammate is.
UPDATE spaces SET enabled = (SELECT count(*) FROM channel_identities ci
                              WHERE ci.principal_id = spaces.principal_id AND ci.status = 'teammate') > 0
 WHERE kind = 'person';

-- Chats a linked account had in a chat app stay with the account on the
-- web, but no chat routes to them any more.
UPDATE sessions SET channel_key = NULL, chat = NULL
 WHERE origin IN ('telegram', 'discord', 'slack')
   AND principal_id IN (SELECT id FROM principals WHERE kind = 'user');

-- Web accounts have no chat app now: their schedules post on the web.
UPDATE schedules SET deliver = json('{"channel":"web","chat_id":"","kind":""}')
 WHERE deliver IS NOT NULL AND json_extract(deliver, '$.channel') <> 'web'
   AND principal_id IN (SELECT id FROM principals WHERE kind = 'user');

-- A teammate's schedules post in their own direct messages.
UPDATE schedules SET deliver = NULL
 WHERE deliver IS NOT NULL
   AND principal_id IN (SELECT id FROM principals WHERE kind = 'person')
   AND NOT EXISTS (SELECT 1 FROM channel_identities ci
                    WHERE ci.principal_id = schedules.principal_id
                      AND json_extract(schedules.deliver, '$.channel') = ci.channel
                      AND json_extract(schedules.deliver, '$.chat_id') IN (ci.dm_chat_id, ci.external_id));

-- A channel's schedule that posted anywhere but the channel (by default the
-- owner's direct messages) would now post in the channel for everyone to
-- see, so it is paused until an admin turns it back on.
UPDATE schedules SET deliver = NULL, enabled = 0
 WHERE principal_id IN (SELECT principal_id FROM spaces WHERE kind = 'group')
   AND NOT EXISTS (SELECT 1 FROM spaces s
                    WHERE s.principal_id = schedules.principal_id
                      AND json_extract(schedules.deliver, '$.channel') = s.channel
                      AND json_extract(schedules.deliver, '$.chat_id') = s.chat_id);

-- Open requests of channels and teammates go to the team's approvers.
UPDATE approvals
   SET approver = json_set(approver, '$.user_id', '', '$.team', json('true')),
       decision = json_set(decision, '$.approver.user_id', '', '$.approver.team', json('true'))
 WHERE status IN ('pending', 'parked')
   AND principal_id IN (SELECT id FROM principals WHERE kind IN ('space', 'person'))
   AND json_extract(approver, '$.admin_only') = 0
   AND COALESCE(json_array_length(approver, '$.users'), 0) = 0
   AND COALESCE(json_array_length(approver, '$.roles'), 0) = 0;

-- Runs and model calls of channels and teammates belong to no account, so
-- reports and resumed runs bill them to the principal.
UPDATE runs SET user_id = '' WHERE principal_id IN (SELECT id FROM principals WHERE kind <> 'user');
UPDATE llm_calls SET user_id = '' WHERE principal_id IN (SELECT id FROM principals WHERE kind <> 'user');

-- Spending is billed to the principal (a user principal's id is the user's).
ALTER TABLE spend_daily RENAME COLUMN user_id TO principal_id;

-- A message that carries a card, a link-only card, or a waiting line.
ALTER TABLE approval_messages ADD COLUMN kind TEXT NOT NULL DEFAULT 'card' CHECK (kind IN ('card','link','notice'));

ALTER TABLE users DROP COLUMN preferred_chat;
ALTER TABLE users DROP COLUMN approvals_web_only;
DROP TABLE pairing_codes;

INSERT INTO meta(k, v)
SELECT 'team_access_report', json_object('at', CAST(unixepoch('subsec') * 1000 AS INTEGER),
         'linked', linked, 'approvers', approvers, 'disabled_accounts', disabled_accounts, 'guests', guests,
         'telegram_guests', telegram_guests, 'guests_blocked', guests_blocked, 'schedules_paused', schedules_paused)
  FROM m_report
 WHERE linked + disabled_accounts + guests + guests_blocked + schedules_paused > 0;

UPDATE m_rows SET after = CASE name
    WHEN 'sessions' THEN (SELECT count(*) FROM sessions)
    WHEN 'messages' THEN (SELECT count(*) FROM messages)
    WHEN 'runs' THEN (SELECT count(*) FROM runs)
    WHEN 'approvals' THEN (SELECT count(*) FROM approvals)
    WHEN 'grants' THEN (SELECT count(*) FROM grants)
    WHEN 'schedules' THEN (SELECT count(*) FROM schedules)
    WHEN 'memory_events' THEN (SELECT count(*) FROM memory_events)
    WHEN 'uploads' THEN (SELECT count(*) FROM uploads)
    WHEN 'knowledge_repos' THEN (SELECT count(*) FROM knowledge_repos)
    WHEN 'release_deploys' THEN (SELECT count(*) FROM release_deploys)
    WHEN 'llm_calls' THEN (SELECT count(*) FROM llm_calls)
    WHEN 'principals' THEN (SELECT count(*) FROM principals)
    WHEN 'spaces' THEN (SELECT count(*) FROM spaces)
    WHEN 'channel_identities' THEN (SELECT count(*) FROM channel_identities)
  END;
DROP TABLE m_rows;
DROP TABLE m_report;
DROP TABLE m_link;
