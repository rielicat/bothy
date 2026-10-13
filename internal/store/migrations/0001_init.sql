-- 0001_init.sql. Times: unix ms INTEGER (0 = unset). Money: micro-USD INTEGER.
-- JSON: TEXT with json_valid checks. Applied in one transaction; the migrator
-- sets PRAGMA user_version = 1 afterwards.

CREATE TABLE meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
) STRICT, WITHOUT ROWID;
-- keys: install_id (store, first open), setup_token_hash, setup_token_exp, setup_grant_hash,
--       setup_grant_exp, setup_done (auth), paused (runner), approval_mac_key (policy, first
--       boot, 32 random bytes hex), constitution_version (runner), agent.spotlight_key (runner,
--       first use, 32 random bytes hex), agent.workspace_untrusted.<ws> (runner, "1" once the
--       workspace holds files from untrusted sources; never cleared),
--       channel.telegram.offset, channel.discord.resume (adapters; JSON session_id, seq, url)

CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL COLLATE NOCASE UNIQUE,
  display_name  TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL CHECK (role IN ('owner','admin','member')),
  pw_hash       TEXT NOT NULL,
  timezone      TEXT NOT NULL DEFAULT 'UTC',
  disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0,1)),
  failed_logins INTEGER NOT NULL DEFAULT 0,
  locked_until  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX users_one_owner ON users(role) WHERE role = 'owner';

CREATE TABLE principals (
  id           TEXT PRIMARY KEY,               -- user principals: same string as users.id
  kind         TEXT NOT NULL CHECK (kind IN ('user','space')),
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,  -- owner for spaces
  display_name TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL UNIQUE,
  tool_profile TEXT NOT NULL CHECK (tool_profile IN ('standard','restricted')),  -- no default: users insert 'standard', spaces 'restricted'
  created_at   INTEGER NOT NULL,
  CHECK (kind = 'space' OR id = user_id)
) STRICT;
CREATE INDEX principals_user ON principals(user_id);

CREATE TABLE spaces (
  principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('group','guest')),
  channel      TEXT NOT NULL,
  chat_id      TEXT NOT NULL,
  chat_kind    TEXT NOT NULL CHECK (chat_kind IN ('dm','group')),
  title        TEXT NOT NULL DEFAULT '',
  owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  enabled      INTEGER NOT NULL DEFAULT 0,
  mention_only INTEGER NOT NULL DEFAULT 1,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  UNIQUE (channel, chat_id)
) STRICT;

CREATE TABLE channel_identities (
  channel        TEXT NOT NULL,
  external_id    TEXT NOT NULL,
  username       TEXT NOT NULL DEFAULT '',
  display_name   TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL CHECK (status IN ('pending','linked','guest','blocked')),
  user_id        TEXT REFERENCES users(id) ON DELETE SET NULL,
  principal_id   TEXT REFERENCES principals(id) ON DELETE SET NULL,
  dm_chat_id     TEXT NOT NULL DEFAULT '',
  first_seen_at  INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  last_notice_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (channel, external_id),
  CHECK (status <> 'linked' OR user_id IS NOT NULL),
  CHECK (status <> 'guest' OR principal_id IS NOT NULL)
) STRICT, WITHOUT ROWID;
CREATE INDEX channel_identities_user ON channel_identities(user_id);

CREATE TABLE pairing_codes (
  code_hash   BLOB PRIMARY KEY,                -- sha256(normalized code)
  purpose     TEXT NOT NULL CHECK (purpose IN ('link_identity','approve_sender')),
  user_id     TEXT REFERENCES users(id) ON DELETE CASCADE,
  channel     TEXT NOT NULL DEFAULT '',
  external_id TEXT NOT NULL DEFAULT '',
  created_by  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  used_at     INTEGER NOT NULL DEFAULT 0,
  attempts    INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;
CREATE INDEX pairing_codes_identity ON pairing_codes(channel, external_id) WHERE purpose = 'approve_sender';

CREATE TABLE auth_sessions (
  id_hash         BLOB PRIMARY KEY,            -- sha256(cookie value)
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf            TEXT NOT NULL,
  ip              TEXT NOT NULL DEFAULT '',
  ua              TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  last_seen_at    INTEGER NOT NULL,
  idle_expires_at INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX auth_sessions_user ON auth_sessions(user_id);
CREATE INDEX auth_sessions_exp ON auth_sessions(expires_at);

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,               -- tok_<26>
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  secret_hash  BLOB NOT NULL UNIQUE,           -- sha256(secret); 256-bit secret, no KDF needed
  scopes       TEXT NOT NULL CHECK (json_valid(scopes)),
  on_approval  TEXT NOT NULL DEFAULT 'deny' CHECK (on_approval IN ('deny','wait')),
  rate_per_min INTEGER NOT NULL DEFAULT 30,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  expires_at   INTEGER NOT NULL,
  revoked_at   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX api_tokens_user ON api_tokens(user_id);

CREATE TABLE sessions (
  id              TEXT PRIMARY KEY,
  principal_id    TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  origin          TEXT NOT NULL CHECK (origin IN ('web','telegram','discord','slack','api','schedule')),
  channel_key     TEXT,
  chat            TEXT CHECK (chat IS NULL OR json_valid(chat)),
  title           TEXT NOT NULL DEFAULT '',
  model_slot      TEXT NOT NULL DEFAULT 'main',
  model_override  TEXT NOT NULL DEFAULT '',
  system_snapshot TEXT NOT NULL DEFAULT '',
  tools_snapshot  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tools_snapshot)),
  snapshot_hash   TEXT NOT NULL DEFAULT '',
  summary         TEXT NOT NULL DEFAULT '',
  summary_upto    INTEGER NOT NULL DEFAULT 0,
  epoch           INTEGER NOT NULL DEFAULT 0,
  memory_seen     INTEGER NOT NULL DEFAULT 0,
  pins_seen       INTEGER NOT NULL DEFAULT 0,
  taint           INTEGER NOT NULL DEFAULT 0,
  hidden          INTEGER NOT NULL DEFAULT 0,
  archived        INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  last_message_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE UNIQUE INDEX sessions_channel_key ON sessions(principal_id, channel_key)
  WHERE channel_key IS NOT NULL AND archived = 0;
CREATE INDEX sessions_recent ON sessions(principal_id, archived, hidden, last_message_at DESC);
-- Hidden one-shot api sessions still due for retention (store/retention.go).
CREATE INDEX sessions_hidden_api ON sessions(max(created_at, updated_at, last_message_at))
  WHERE hidden = 1 AND origin = 'api' AND archived = 0;

CREATE TABLE session_urls (                      -- URL provenance for web_fetch in exposed sessions
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  url_hash   BLOB NOT NULL,                      -- sha256(normalized URL)
  source     TEXT NOT NULL CHECK (source IN ('search','user')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, url_hash)
) STRICT, WITHOUT ROWID;

-- INTEGER PRIMARY KEY is required: FTS5 external content keys on it, and the
-- implicit rowid of a TEXT-PK table can change under VACUUM / VACUUM INTO.
CREATE TABLE messages (
  id          INTEGER PRIMARY KEY,
  session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  run_id      TEXT,
  role        TEXT NOT NULL CHECK (role IN ('user','assistant','tool')),
  kind        TEXT NOT NULL DEFAULT 'normal' CHECK (kind IN ('normal','summary','steer','notice')),
  provenance  TEXT NOT NULL CHECK (provenance IN ('user','untrusted','tool','system')),
  sender      TEXT NOT NULL DEFAULT '',
  parts       TEXT NOT NULL CHECK (json_valid(parts)),
  text        TEXT NOT NULL DEFAULT '',          -- PlainText() projection for FTS (no tool JSON)
  tokens      INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  UNIQUE (session_id, seq)
) STRICT;

CREATE VIRTUAL TABLE messages_fts USING fts5(
  text, content='messages', content_rowid='id', tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER messages_au AFTER UPDATE OF text ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.id, old.text);
  INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;

CREATE TABLE pins (
  id         INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  text       TEXT NOT NULL CHECK (length(text) <= 2000),
  source     TEXT NOT NULL CHECK (source IN ('user','auto')),
  msg_seq    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX pins_session ON pins(session_id, id);

CREATE TABLE schedules (
  id           TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('cron','heartbeat','once')),
  spec         TEXT NOT NULL,
  tz           TEXT NOT NULL DEFAULT 'UTC',
  active_hours TEXT NOT NULL DEFAULT '',
  prompt       TEXT NOT NULL,
  session_id   TEXT REFERENCES sessions(id) ON DELETE SET NULL,
  deliver      TEXT CHECK (deliver IS NULL OR json_valid(deliver)),
  model_slot   TEXT NOT NULL DEFAULT 'main',
  limits       TEXT NOT NULL CHECK (json_valid(limits)),
  missed_run   TEXT NOT NULL DEFAULT 'skip' CHECK (missed_run IN ('skip','once')),
  enabled      INTEGER NOT NULL DEFAULT 1,
  created_by   TEXT NOT NULL,
  failures     INTEGER NOT NULL DEFAULT 0,
  next_run_at  INTEGER NOT NULL DEFAULT 0,
  last_run_at  INTEGER NOT NULL DEFAULT 0,
  last_status  TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX schedules_due ON schedules(next_run_at) WHERE enabled = 1;

CREATE TABLE runs (
  id                 TEXT PRIMARY KEY,
  session_id         TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  principal_id       TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  user_id            TEXT NOT NULL,
  trigger            TEXT NOT NULL CHECK (trigger IN ('message','schedule','heartbeat','api','resume')),
  schedule_id        TEXT REFERENCES schedules(id) ON DELETE SET NULL,
  status             TEXT NOT NULL CHECK (status IN ('queued','running','waiting_approval','parked','done',
                       'stopped','failed','budget_exceeded','loop_detected','interrupted','skipped')),
  error              TEXT NOT NULL DEFAULT '',
  steps              INTEGER NOT NULL DEFAULT 0,
  tool_calls         INTEGER NOT NULL DEFAULT 0,
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cost_micros        INTEGER NOT NULL DEFAULT 0,
  models             TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(models)),
  limits             TEXT NOT NULL CHECK (json_valid(limits)),
  parked_on          TEXT NOT NULL DEFAULT '',
  stopped_by         TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  started_at         INTEGER NOT NULL DEFAULT 0,
  finished_at        INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX runs_session ON runs(session_id, created_at DESC);
CREATE INDEX runs_schedule ON runs(schedule_id, created_at DESC) WHERE schedule_id IS NOT NULL;
CREATE INDEX runs_principal_time ON runs(principal_id, created_at DESC);
CREATE INDEX runs_open ON runs(status) WHERE status IN ('queued','running','waiting_approval','parked');
-- DB guard for "one active run per session" (the in-memory worker is the primary guard).
CREATE UNIQUE INDEX runs_one_active ON runs(session_id) WHERE status IN ('running','waiting_approval');

CREATE TABLE llm_calls (
  id                 TEXT PRIMARY KEY,
  run_id             TEXT REFERENCES runs(id) ON DELETE CASCADE,  -- NULL for passthrough/probe
  principal_id       TEXT NOT NULL DEFAULT '',
  user_id            TEXT NOT NULL,
  step               INTEGER NOT NULL DEFAULT 0,
  purpose            TEXT NOT NULL CHECK (purpose IN ('agent','compaction','title','heartbeat','probe','passthrough')),
  provider           TEXT NOT NULL,
  model              TEXT NOT NULL,
  slot               TEXT NOT NULL DEFAULT '',
  attempts           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(attempts)),
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
  usage_estimated    INTEGER NOT NULL DEFAULT 0,
  cost_micros        INTEGER NOT NULL DEFAULT 0,
  price_known        INTEGER NOT NULL DEFAULT 1,
  breakdown          TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(breakdown)),
  prefix_hash        TEXT NOT NULL DEFAULT '',
  ttft_ms            INTEGER NOT NULL DEFAULT 0,
  latency_ms         INTEGER NOT NULL DEFAULT 0,
  stop_reason        TEXT NOT NULL DEFAULT '',
  error              TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL
) STRICT;
CREATE INDEX llm_calls_run ON llm_calls(run_id, step);
CREATE INDEX llm_calls_user_time ON llm_calls(user_id, created_at);
CREATE INDEX llm_calls_time ON llm_calls(created_at);

CREATE TABLE tool_calls (
  id             TEXT PRIMARY KEY,               -- tcl_<26>
  run_id         TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  session_id     TEXT NOT NULL,
  principal_id   TEXT NOT NULL,
  step           INTEGER NOT NULL,
  call_id        TEXT NOT NULL,
  tool           TEXT NOT NULL,
  source         TEXT NOT NULL,
  side_effect    TEXT NOT NULL,
  action         TEXT CHECK (action IS NULL OR json_valid(action)),
  verdict        TEXT NOT NULL CHECK (verdict IN ('auto','ask','deny','')),
  rule           TEXT NOT NULL DEFAULT '',
  approval_id    TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL CHECK (status IN ('planned','waiting','approved','denied','running','ok',
                   'error','timeout','stopped','invalid')),
  result_preview TEXT NOT NULL DEFAULT '',
  result_bytes   INTEGER NOT NULL DEFAULT 0,
  full_ref       TEXT NOT NULL DEFAULT '',
  untrusted      INTEGER NOT NULL DEFAULT 0,
  duration_ms    INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  finished_at    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX tool_calls_run ON tool_calls(run_id, step);

CREATE TABLE approvals (
  id           TEXT PRIMARY KEY,
  run_id       TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  session_id   TEXT NOT NULL,
  principal_id TEXT NOT NULL,
  tool_call_id TEXT NOT NULL,
  action       TEXT NOT NULL CHECK (json_valid(action)),
  decision     TEXT NOT NULL CHECK (json_valid(decision)),
  card         TEXT NOT NULL CHECK (json_valid(card)),
  approver     TEXT NOT NULL CHECK (json_valid(approver)),
  chat         TEXT CHECK (chat IS NULL OR json_valid(chat)),
  status       TEXT NOT NULL CHECK (status IN ('pending','allowed','denied','expired','parked','cancelled')),
  choice       TEXT NOT NULL DEFAULT '',
  on_timeout   TEXT NOT NULL CHECK (on_timeout IN ('deny','park')),
  decided_by   TEXT NOT NULL DEFAULT '',
  decided_via  TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  park_until   INTEGER NOT NULL DEFAULT 0,
  decided_at   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX approvals_open ON approvals(principal_id, status) WHERE status IN ('pending','parked');
CREATE INDEX approvals_due ON approvals(expires_at) WHERE status = 'pending';
CREATE INDEX approvals_parked ON approvals(park_until) WHERE status = 'parked';
-- First contact (Approvals.Seen); the expressions match store/runs.go.
CREATE INDEX approvals_seen_recipient ON approvals(principal_id, json_extract(action, '$.recipient')) WHERE status = 'allowed';
CREATE INDEX approvals_seen_host ON approvals(principal_id, json_extract(action, '$.host')) WHERE status = 'allowed';
CREATE INDEX approvals_run ON approvals(run_id);

CREATE TABLE approval_messages (                 -- chat messages that carry a card; presses must come from these
  approval_id TEXT NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
  channel     TEXT NOT NULL,
  chat_id     TEXT NOT NULL,
  thread_id   TEXT NOT NULL DEFAULT '',
  message_id  TEXT NOT NULL,
  truncated   INTEGER NOT NULL DEFAULT 0,        -- 1 => deny-only from chat
  PRIMARY KEY (approval_id, channel, chat_id, message_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE grants (
  id                 TEXT PRIMARY KEY,
  principal_id       TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  scope              TEXT NOT NULL CHECK (scope IN ('session','schedule','principal')),
  scope_id           TEXT NOT NULL DEFAULT '',
  kind               TEXT NOT NULL CHECK (kind IN ('tool','domain','recipient')),
  tool               TEXT NOT NULL DEFAULT '',
  pattern            TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(pattern)),
  value              TEXT NOT NULL DEFAULT '',
  max_se             TEXT NOT NULL,
  allow_when_tainted INTEGER NOT NULL DEFAULT 0,
  uses               INTEGER NOT NULL DEFAULT 0,
  max_uses           INTEGER NOT NULL DEFAULT 0,
  created_by         TEXT NOT NULL,
  created_at         INTEGER NOT NULL,
  expires_at         INTEGER NOT NULL DEFAULT 0,
  revoked_at         INTEGER NOT NULL DEFAULT 0,
  CHECK ((scope = 'principal') = (scope_id = '')),
  CHECK (kind <> 'tool' OR (tool <> '' AND pattern <> '{}')),   -- never a whole-tool grant
  CHECK (allow_when_tainted = 0 OR kind = 'recipient')
) STRICT;
CREATE INDEX grants_lookup ON grants(principal_id, scope, scope_id) WHERE revoked_at = 0;

CREATE TABLE vault_keys (
  id          INTEGER PRIMARY KEY,
  wrapped_dek BLOB NOT NULL,                     -- nonce || AES-256-GCM(KEK, DEK)
  kek_check   BLOB NOT NULL,                     -- nonce || AES-256-GCM(KEK, "bothy-kek-check")
  kek_source  TEXT NOT NULL,
  active      INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX vault_keys_active ON vault_keys(active) WHERE active = 1;

CREATE TABLE vault_secrets (
  scope        TEXT NOT NULL,
  name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^a-z0-9_.-]*'),
  key_id       INTEGER NOT NULL REFERENCES vault_keys(id),
  version      INTEGER NOT NULL,
  nonce        BLOB NOT NULL,
  ciphertext   BLOB NOT NULL,                    -- AAD = scope || 0x00 || name || 0x00 || version
  hint         TEXT NOT NULL DEFAULT '',
  created_by   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (scope, name)
) STRICT, WITHOUT ROWID;

CREATE TABLE mcp_tool_pins (
  server        TEXT NOT NULL,
  tool          TEXT NOT NULL,
  hash          TEXT NOT NULL,
  definition    TEXT NOT NULL CHECK (json_valid(definition)),
  status        TEXT NOT NULL CHECK (status IN ('pending','approved','superseded','disabled')),
  side_effect   TEXT NOT NULL DEFAULT 'external_send',
  field_map     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(field_map)),
  private       INTEGER NOT NULL DEFAULT 0,
  approved_by   TEXT NOT NULL DEFAULT '',
  first_seen_at INTEGER NOT NULL,
  approved_at   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (server, tool, hash)
) STRICT, WITHOUT ROWID;
CREATE INDEX mcp_pins_status ON mcp_tool_pins(server, status);

CREATE TABLE mcp_installs (
  install_id TEXT PRIMARY KEY,                   -- in_<26>
  server     TEXT NOT NULL,
  version    TEXT NOT NULL,
  lock_hash  TEXT NOT NULL DEFAULT '',
  lock       BLOB,
  tree_hash  TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL CHECK (status IN ('installing','ready','failed','drifted')),
  error      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX mcp_installs_server ON mcp_installs(server, created_at DESC);

CREATE TABLE skills (
  name          TEXT PRIMARY KEY,
  description   TEXT NOT NULL,
  path          TEXT NOT NULL,
  hash          TEXT NOT NULL,                   -- sha256 over the sorted file tree
  approved_hash TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL CHECK (status IN ('pending','enabled','disabled','changed')),
  tokens        INTEGER NOT NULL DEFAULT 0,
  installed_by  TEXT NOT NULL DEFAULT '',
  approved_by   TEXT NOT NULL DEFAULT '',
  installed_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE memory_events (
  id           INTEGER PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  file         TEXT NOT NULL CHECK (file IN ('MEMORY','USER','HEARTBEAT')),
  op           TEXT NOT NULL CHECK (op IN ('add','replace','remove','edit')),
  before       TEXT NOT NULL DEFAULT '',
  after        TEXT NOT NULL DEFAULT '',
  run_id       TEXT NOT NULL DEFAULT '',
  actor        TEXT NOT NULL,
  provenance   TEXT NOT NULL DEFAULT 'user',
  created_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX memory_events_p ON memory_events(principal_id, id);

CREATE TABLE spend_daily (
  user_id       TEXT NOT NULL,                   -- billing user (space owner for spaces)
  day           TEXT NOT NULL,                   -- YYYY-MM-DD in config timezone
  cost_micros   INTEGER NOT NULL DEFAULT 0,
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  calls         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, day)
) STRICT, WITHOUT ROWID;

CREATE TABLE inbound_dedup (
  channel TEXT NOT NULL,
  msg_id  TEXT NOT NULL,
  seen_at INTEGER NOT NULL,
  PRIMARY KEY (channel, msg_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE uploads (
  id           TEXT PRIMARY KEY,                 -- upl_<26>
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  session_id   TEXT NOT NULL,
  name         TEXT NOT NULL,
  mime         TEXT NOT NULL,
  size         INTEGER NOT NULL,
  ref          TEXT NOT NULL,                    -- workspace-relative path
  created_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE egress_log (
  id           INTEGER PRIMARY KEY,
  ts           INTEGER NOT NULL,
  dialer       TEXT NOT NULL CHECK (dialer IN ('box','public','system')),
  dest         TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL DEFAULT '',
  principal_id TEXT NOT NULL DEFAULT '',
  run_id       TEXT NOT NULL DEFAULT '',
  host         TEXT NOT NULL,
  port         INTEGER NOT NULL,
  ip           TEXT NOT NULL DEFAULT '',
  decision     TEXT NOT NULL CHECK (decision IN ('allow','deny','held_allow','held_deny')),
  reason       TEXT NOT NULL DEFAULT '',
  bytes_up     INTEGER NOT NULL DEFAULT 0,
  bytes_down   INTEGER NOT NULL DEFAULT 0,
  duration_ms  INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX egress_log_ts ON egress_log(ts);
CREATE INDEX egress_log_session ON egress_log(session_id, ts);

CREATE TABLE audit_log (
  id         INTEGER PRIMARY KEY,
  ts         INTEGER NOT NULL,
  actor      TEXT NOT NULL,
  action     TEXT NOT NULL,
  target     TEXT NOT NULL DEFAULT '',
  outcome    TEXT NOT NULL DEFAULT 'ok' CHECK (outcome IN ('ok','denied','error')),
  session_id TEXT NOT NULL DEFAULT '',
  run_id     TEXT NOT NULL DEFAULT '',
  ip         TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(detail)),
  prev_hash  BLOB NOT NULL,
  hash       BLOB NOT NULL                       -- sha256(prev_hash || canonical JSON of the row without hashes)
) STRICT;
CREATE INDEX audit_ts ON audit_log(ts);
CREATE INDEX audit_action ON audit_log(action, ts);
CREATE INDEX audit_actor ON audit_log(actor, ts);
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit_log
  BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit_log
  BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
