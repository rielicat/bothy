# audit

`core.Auditor` (owner: M1): the only writer of `audit_log` in the gateway. `Record`
validates an entry, masks secrets inside its detail (string values, keys and
numbers, so the detail stays valid JSON) and stores it as canonical JSON;
`auth.*`, `config.*`, `approval.*` and `secret.*` entries are written before
`Record` returns (one that fails is reported and never written later), the rest
are batched and flushed by `Run` every 250 ms and once more on shutdown. Each
flush reads the head of the log inside its write transaction, so CLI processes
appending to the same database keep the chain intact, and appends its rows with
`hash = sha256(prev_hash ‖ canonical JSON of the row without the hashes)`, the
first row of a fresh log chaining from 32 zero bytes. `Verify` returns the id of
the first row whose hash or link is wrong (0 when intact); the table's triggers
already reject UPDATE and DELETE, so this catches edits made with those
triggers lifted. `Archive(ctx, dbPath, before)` serves the offline
`bothy audit archive`: it refuses a broken chain, exports the oldest rows up to
the cutoff to a 0600 JSONL file under `<data>/backups/`, then deletes them and
appends an `audit.archived` anchor (range, count, last archived hash) in one
transaction, which keeps `Verify` green.
