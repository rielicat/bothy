-- 0003_knowledge.sql (v0.2 F6): the repository index and the metadata of
-- per-repo project notes. Notes text lives in
-- <data>/knowledge/<principal>/<owner>/<repo>/NOTES.md, never in a repo.

CREATE TABLE knowledge_repos (
  id               INTEGER PRIMARY KEY,
  principal_id     TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  repo             TEXT NOT NULL CHECK (repo GLOB '?*/?*' AND length(repo) <= 201),  -- "owner/name", lowercase
  source           TEXT NOT NULL CHECK (source IN ('github','workspace')),
  dir              TEXT NOT NULL,                  -- workspace-relative directory of the indexed copy
  sha              TEXT NOT NULL DEFAULT '',       -- commit of the latest requested index
  status           TEXT NOT NULL CHECK (status IN ('queued','indexing','ready','error')),
  error            TEXT NOT NULL DEFAULT '',
  files            INTEGER NOT NULL DEFAULT 0,
  chunks           INTEGER NOT NULL DEFAULT 0,
  bytes            INTEGER NOT NULL DEFAULT 0,
  skipped          INTEGER NOT NULL DEFAULT 0,
  truncated        INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0,1)),
  indexed_at       INTEGER NOT NULL DEFAULT 0,
  notes_status     TEXT NOT NULL DEFAULT 'none' CHECK (notes_status IN ('none','drafting','ready','error')),
  notes_error      TEXT NOT NULL DEFAULT '',
  notes_untrusted  INTEGER NOT NULL DEFAULT 0 CHECK (notes_untrusted IN (0,1)),
  notes_updated_at INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  UNIQUE (principal_id, repo)
) STRICT;

-- One row per indexed file; hash drives incremental re-indexing.
CREATE TABLE knowledge_files (
  repo_id INTEGER NOT NULL REFERENCES knowledge_repos(id) ON DELETE CASCADE,
  path    TEXT NOT NULL,
  hash    TEXT NOT NULL,                           -- chunker version + sha256 of the bytes
  size    INTEGER NOT NULL,
  lang    TEXT NOT NULL DEFAULT '',
  chunks  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (repo_id, path)
) STRICT, WITHOUT ROWID;

-- INTEGER PRIMARY KEY: the FTS5 external content keys on it (see messages).
CREATE TABLE knowledge_chunks (
  id         INTEGER PRIMARY KEY,
  repo_id    INTEGER NOT NULL REFERENCES knowledge_repos(id) ON DELETE CASCADE,
  sha        TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL,
  start_line INTEGER NOT NULL CHECK (start_line >= 1),
  end_line   INTEGER NOT NULL CHECK (end_line >= start_line),
  kind       TEXT NOT NULL,                        -- heading | func | method | type | class | header | text | …
  lang       TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL,
  terms      TEXT NOT NULL DEFAULT ''              -- identifier parts (camelCase split) for recall
) STRICT;
CREATE INDEX knowledge_chunks_file ON knowledge_chunks(repo_id, path);

CREATE VIRTUAL TABLE knowledge_fts USING fts5(
  path, title, body, terms, content='knowledge_chunks', content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER knowledge_chunks_ai AFTER INSERT ON knowledge_chunks BEGIN
  INSERT INTO knowledge_fts(rowid, path, title, body, terms) VALUES (new.id, new.path, new.title, new.body, new.terms);
END;
CREATE TRIGGER knowledge_chunks_ad AFTER DELETE ON knowledge_chunks BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, path, title, body, terms) VALUES ('delete', old.id, old.path, old.title, old.body, old.terms);
END;
-- Relabelling sha after a re-index must not rewrite the FTS rows.
CREATE TRIGGER knowledge_chunks_au AFTER UPDATE OF path, title, body, terms ON knowledge_chunks BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, path, title, body, terms) VALUES ('delete', old.id, old.path, old.title, old.body, old.terms);
  INSERT INTO knowledge_fts(rowid, path, title, body, terms) VALUES (new.id, new.path, new.title, new.body, new.terms);
END;

-- Repos a session worked on, and the notes version its turn-context carries.
CREATE TABLE knowledge_sessions (
  session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  repo_id        INTEGER NOT NULL REFERENCES knowledge_repos(id) ON DELETE CASCADE,
  touched_at     INTEGER NOT NULL,
  injected_epoch INTEGER NOT NULL DEFAULT -1,
  injected_hash  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (session_id, repo_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX knowledge_sessions_repo ON knowledge_sessions(repo_id);

-- llm_calls.purpose gains 'notes' (project notes drafts, metered outside any
-- run). SQLite cannot alter a CHECK, so the table is rebuilt with the same
-- columns and indexes; no table, trigger or view references it. rowid is
-- copied because per-run listings break ties on it.
CREATE TABLE llm_calls_v3 (
  id                 TEXT PRIMARY KEY,
  run_id             TEXT REFERENCES runs(id) ON DELETE CASCADE,  -- NULL for passthrough/probe/notes
  principal_id       TEXT NOT NULL DEFAULT '',
  user_id            TEXT NOT NULL,
  step               INTEGER NOT NULL DEFAULT 0,
  purpose            TEXT NOT NULL CHECK (purpose IN ('agent','compaction','title','heartbeat','probe','passthrough','notes')),
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
INSERT INTO llm_calls_v3 (rowid, id, run_id, principal_id, user_id, step, purpose, provider, model, slot, attempts,
  input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, usage_estimated, cost_micros,
  price_known, breakdown, prefix_hash, ttft_ms, latency_ms, stop_reason, error, created_at)
SELECT rowid, id, run_id, principal_id, user_id, step, purpose, provider, model, slot, attempts,
  input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, usage_estimated, cost_micros,
  price_known, breakdown, prefix_hash, ttft_ms, latency_ms, stop_reason, error, created_at
FROM llm_calls;
DROP TABLE llm_calls;
ALTER TABLE llm_calls_v3 RENAME TO llm_calls;
CREATE INDEX llm_calls_run ON llm_calls(run_id, step);
CREATE INDEX llm_calls_user_time ON llm_calls(user_id, created_at);
CREATE INDEX llm_calls_time ON llm_calls(created_at);
