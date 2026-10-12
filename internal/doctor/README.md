# internal/doctor

`doctor.Runner` runs the `core.Check`s every module contributes (SPEC §10) for
`bothy doctor`, `bothy security-audit` and `/api/doctor`. Checks run four at a
time with a 15 s budget each; a check that panics or overruns becomes a medium
finding instead of aborting the run. `Run(security)` keeps only findings marked
`Security` for the security audit and sorts by severity, then id. `Fix` routes
each auto-fixable finding to the check that reported it (or the check with the
same id) and returns the ids it fixed; a fix that panics counts as not fixed.
`ConfigChecks` adds `config.errors` and `config.safe_mode` from
`config.Admin.Effective()`, and `Print` renders text or JSON and returns exit
code 1 when any finding is high or critical.
