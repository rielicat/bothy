# audit

`core.Auditor` (owner: M1): the only writer of `audit_log`. `Record` validates an
entry, redacts its detail and stores it as canonical JSON; `auth.*`, `config.*`,
`approval.*` and `secret.*` entries are written before `Record` returns, the
rest are batched and flushed by `Run` every 250 ms and once more on shutdown.
Each flush appends its rows in one transaction with
`hash = sha256(prev_hash ‖ canonical JSON of the row without id and hashes)`,
the first row of a fresh log chaining from 32 zero bytes. `Verify` returns the
id of the first row whose hash or link is wrong (0 when intact); the table's
triggers already reject UPDATE and DELETE, so this catches edits made with
those triggers lifted. `Archive(ctx, dbPath, before)` serves the offline
`bothy audit archive`: it refuses a broken chain, exports the oldest rows up to
the cutoff to a 0600 JSONL file under `<data>/backups/`, then deletes them and
appends an `audit.archived` anchor (range, count, last archived hash) in one
transaction, which keeps `Verify` green.
