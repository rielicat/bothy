# tools

Build-time helpers in a separate Go module, so their dependencies never reach
the `bothy` binary or the root `go.mod`. `typesgen` runs tygo over
`internal/core`, `internal/config` (API types only) and `internal/httpapi/dto`
as configured in `tygo.yaml` and merges the result into
`web/src/lib/api/types.gen.ts` (`make gen`; CI fails when the committed file is
stale). `cooldown` enforces the 14-day dependency cooldown
(`scripts/check-cooldown.sh`). `catalog-gen/` (M2) regenerates the provider
catalog at release time.
