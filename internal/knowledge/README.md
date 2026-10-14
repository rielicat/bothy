# internal/knowledge

`knowledge.Service` implements `core.Knowledge` (v0.2 brief §6): Bothy's own
index of repositories and its own project notes, kept outside the repositories.

**Indexing.** A `repo.checkout` event (`core.RepoCheckoutData` with
`Event.Principal`, published by the GitHub module after `repo_checkout`;
`dir` may be host-absolute, box-side `/workspace/...` or workspace-relative,
and a `workspace_id` other than the principal's is ignored) or `POST /api/knowledge/repos` (any directory of the
caller's workspace; a default `local/<name>` already naming another folder is
a 409) queues an index. Two workers walk the directory read-only through
`os.Root`, never following symlinks and opening files with `O_NONBLOCK` (a FIFO
a box plants cannot park a worker), and skip VCS metadata, vendored and build
directories, lockfiles, minified and generated files (by name and by
"generated, do not edit" headers), binaries (NUL or invalid UTF-8),
`.gitignore`d paths (nested files, negation, `**`, anchoring) and
`.gitattributes` `linguist-generated`/`linguist-vendored` paths. One walk
loads at most 10000 ignore patterns and spends at most 50M matching cells, so
hostile rules end it as a truncated index. Size caps
(`knowledge.max_file_kb`, `max_files`, `max_repo_mb`) count skipped files and
mark a truncated walk. Re-indexing is incremental: a file whose chunker version
plus sha256 is unchanged is not re-chunked; changed files are replaced and
vanished ones deleted, in batched transactions, then every chunk is relabelled
with the new commit. A restart requeues interrupted indexes. A finished index
of a GitHub checkout calls `Deps.Indexed`, which `cmd/bothy` uses to record
the commit as the repository's `last_indexed_sha`.

**Chunking** is regex-light: markdown by headings (levels 1-3, fenced code and
front matter ignored, titles are heading paths), Go, Python, JS/TS, Rust, Java-family, C/C++,
Ruby, PHP, shell, Lua, Elixir and SQL by top-level declarations (doc comments
and decorators attach; methods inside classes and impls become their own
chunks titled `Parent: signature`), TOML/INI sections, YAML top-level keys,
Makefile targets and Dockerfile stages; anything else by paragraphs. Tiny
chunks fold into their neighbour, long ones split into 80-line windows, very
long lines are cut in the stored body. Identifier parts (`NewIndexer` →
`new indexer`) go into a hidden FTS column for recall.

**Search.** The store ANDs quoted query words (no FTS syntax survives) and
falls back to OR, ranked by bm25 with path and title weighted up; the service
narrows each hit to the lines around the first match and cites exactly those
lines as `owner/repo@sha:path#Lx-Ly`. `knowledge_search` (read, `SpaceOK`,
`TaintPrivate|TaintUntrusted`: workspace content, as private as an `fs_read`,
that anyone may have authored; offered once the principal has an indexed repo)
searches the run's own principal only and touches the repo it used.

**Notes** live in `<data>/knowledge/<principal>/<owner>/<repo>/NOTES.md`
(0600, atomic writes, one mutex with the notes state row). After a repo's first
index a background draft (`knowledge.auto_notes`) asks the fast model (main
when no fast model is set, metered as purpose `notes`, daily cap checked) for
fixed sections from the README, build files, two CI workflows and the
top-level layout, all spotlighted as untrusted and passed through the vault
redactor (as is the reply); the result is marked untrusted. The `notes` tool
(`TaintPrivate`, like memory) reads (read) or edits (append to a section, replace
exact text) them; edits plan a diff, use `Action.Kind` memory so policy's
tainted-memory floor asks for approval once untrusted content is in the
session, re-apply against the current file in `Exec`, and keep the notes
untrusted when written under untrusted taint. A user's save from the web UI
(etag-guarded) makes them trusted.

**Turn-context.** `TurnContext` (the runner's hook) adds, most recently
touched repo first and within `knowledge.context_bytes`, the notes of repos the
session worked on, minus their title and cut at a line boundary. Each repo's
notes enter once per compaction epoch and again only when they change, and the
runner commits that only after the turn is persisted, so earlier prompt bytes
never change. Injected notes add `TaintPrivate`; untrusted ones are also
spotlighted and add `TaintUntrusted`. A user purge deletes the principal's
rows under the notes lock before removing its notes, so no draft in flight
writes them back.

`GET /api/knowledge/repos/{id}/files` lists what an index holds. All reads are Viewer-scoped (`core.KnowledgeRepo`, conformance-tested on
SQLite and `knowledgetest`); every index, draft, edit and delete is audited
(`knowledge.*`, never content) and publishes `knowledge.updated`.
