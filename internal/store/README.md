# store

`core.Store` on `modernc.org/sqlite` (owner: M1). `Open` creates `<data>/bothy.db`
(and its `-wal`/`-shm`) 0600, sets the pragmas of SPEC §6 on every pooled
connection, refuses SQLite older than 3.53.4, takes a `VACUUM INTO` backup when
migrations are pending, applies the embedded migrations and writes `meta.install_id`
on the first open. All 24 repositories, plus `Knowledge()` (the v0.2 FTS5
index of `0003_knowledge.sql`, outside `core.Store` so the frozen interface
keeps its fakes),
are hand-written SQL over one writer connection and two `query_only` readers;
`Tx` runs on the writer inside `BEGIN IMMEDIATE`, a nested `Tx` joins it, and
multi-statement writes use a savepoint when already inside a transaction. Every
by-ID read takes a `core.Viewer` and answers `ErrNotFound` outside its
principals (an empty principal list matches nothing). Constraint errors map to
the core sentinels: UNIQUE and PRIMARY KEY become `ErrConflict`; CHECK, FOREIGN
KEY, NOT NULL and STRICT type errors become `ErrInvalid`. `Messages.Search` turns
user input into an AND of quoted FTS5 terms, so no input can inject FTS syntax.
`Backup` writes a 0600 tarball with a `VACUUM INTO` snapshot plus `memory/**`,
`knowledge/**`, `SOUL.md`, `bothy.yaml` and `bothy.ui.yaml` (never the KEK or workspaces),
`RunRetention` prunes hourly (never `audit_log`), `PruneAudit` serves only the
offline `bothy audit archive`, and `Checks` reports `sqlite.version`,
`data.permissions` (auto-fixable) and `backups.none_recent`. Code inside a `Tx`
callback must use the `core.Store` it is given: the writer pool has a single
connection. `conformance.Store` runs against a temp database in the tests.
`DB.GitHub()` returns the `core.GitHubStore` of migration `0002_repos.sql`
(GitHub connections and their repositories, one row per repository whatever
its case), kept outside `core.Store` and checked by `conformance.GitHubStore`.
Migration 0004 adds `approval_votes` (one vote per approver and approval;
multi-approver approvals are also visible to their voters) and
`release_deploys`, served by `Releases()` (`core.ReleaseStore`, outside
`core.Store`). Migration 0005 adds the approver's reason
(`approvals.comment`, `approval_votes.comment`) and a pending sender's
`channel_identities.first_text`; 0006 adds `users.must_change_password`
(`core.User.MustChangePassword`).
