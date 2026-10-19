# Contributing to Bothy

Thanks for helping. Bothy is security software that runs an agent with your
credentials, so the bar for changes is deliberately high. CI enforces the
mechanical rules below (formatting and linters, import and ownership rules,
the dependency cooldown and the DCO sign-off); review upholds the rest, such
as the interface change process and the comment style.

The design lives in [docs/SPEC.md](docs/SPEC.md). Go code, SQL, YAML and route
tables there are normative: implement them as written, and propose changes to
the spec rather than diverging from it quietly.

## Getting started

You need Go (the toolchain in `go.mod` downloads itself), Node 24 for the web
UI (`web/.nvmrc`) and, for images, Docker with buildx.

| Command | What it does |
|---|---|
| `make build` | static binary in `bin/bothy` (`CGO_ENABLED=0`) |
| `make test` | `go test -race ./...` (the race detector needs cgo, so only test binaries use it) |
| `make lint` | gofmt, vet, golangci-lint (includes staticcheck), import and ownership rules |
| `make vuln` | govulncheck |
| `make ui` | builds `web/` into `internal/webui/dist` with `npm ci --ignore-scripts` |
| `make ui-reset` | restores the committed placeholder in `internal/webui/dist` |
| `make gen` | regenerates `web/src/lib/api/types.gen.ts` from `internal/core`, `internal/config` and `internal/httpapi/dto` |
| `make icons` | renders the PNG icons and the default chat-app picture from `web/public/icon.svg` and `icon-maskable.svg` |
| `make e2e` | builds the binary and runs the black-box suite in `e2e/` |
| `make image` | builds the gateway image from `deploy/Dockerfile` |
| `make budgets` | size, cold-start and RSS budgets (SPEC §11.3) |

`internal/webui/dist/index.html` is a committed placeholder so the binary
builds without Node. Never commit a UI build: run `make ui-reset` before
committing. CI checks that the placeholder is what is committed.

## Ownership and branches

v0.1 is built by module owners working in parallel on disjoint paths, listed
in [OWNERSHIP.md](OWNERSHIP.md). Name a module branch `m<N>/<topic>` (for
example `m4/policy-floors`); `scripts/check-ownership.sh` fails the pull
request if it touches a path outside that module. Everything unlisted belongs
to the lead, including `go.mod`, `internal/core`, `internal/config`,
`internal/events`, `internal/httpapi/dto`, the migrations, `internal/testkit`,
`cmd/`, `deploy/` (except `deploy/sandbox/`), CI and scripts.

### Interface change requests (ICR)

When you need a change in a lead-owned file (a new field on a core type, a new
method, a DTO, a test fake):

1. Open an issue titled `ICR: <package>.<symbol>`, for example
   `ICR: core.Run.ParentID`, with the proposed diff and why you need it.
2. Meanwhile, code against a small local adapter inside your own package so
   your work is not blocked.
3. The lead batches approved ICRs once a day into a skeleton patch; rebase
   onto it and delete the adapter.

Schema changes work the same way: ask the lead for a `0002_*.sql` migration;
`0001_init.sql` is never edited.

## Import rules

`scripts/check-imports.sh` enforces SPEC §3:

- `internal/core` imports only the standard library.
- A module imports `core`, `config`, the standard library, its own packages
  and the dependencies SPEC §11.1 approves for it, never another module.
  `httpapi` may also import `doctor` and `httpapi/dto`; channel adapters never
  import `internal/channel`; tests may import `internal/testkit/**` and
  `internal/events`.
- Only `cmd/bothy` wires concrete module packages together.
- `os/exec` only in `internal/sandbox/bwrap`; `unsafe` nowhere.
- No provider SDKs, and no `http.DefaultClient` or `http.Get` in `internal/`:
  every HTTP client comes from `core.Egress`.
- No `core.SystemViewer()` in `internal/httpapi`: handlers pass the actor's
  `Viewer`.

## Dependencies

New dependencies are rare and go through the lead. Every Go module and npm
package version must be at least **14 days old** when it lands, so that a
compromised release has time to be noticed; `scripts/check-cooldown.sh`
checks publish times against proxy.golang.org and the npm registry, and
Renovate waits 14 days before proposing updates. npm installs always use
`--ignore-scripts`. A security fix may skip the cooldown with the lead's
approval: the lead adds `-allow name@version` to the CI step.

## Code style

- `gofmt`, `go vet` and `golangci-lint` clean; idiomatic Go; small packages.
- **Comments explain only the non-obvious why**: rationale, gotchas,
  invariants, surprising constraints. Do not restate what the code says, do
  not narrate steps, and do not put pull-request context ("added for X",
  "temporary until Y lands") in the source; that belongs in the commit
  message. Match the comment density of the surrounding file. When in doubt,
  leave it out.
- Errors wrap the `core` sentinels with `%w`; secrets never reach logs (the
  slog handler redacts, but do not rely on it).
- Unit tests use `httptest` and the fakes in `internal/testkit`; no real
  network, no sleeps where `testing/synctest` works. `go test -race` must pass
  for your packages on their own.

## Commits and pull requests

- Conventional commit messages (`feat(policy): …`, `fix(egress): …`), with
  the why in the body when it is not obvious.
- Every commit carries a Developer Certificate of Origin sign-off
  (`git commit -s`) by its author, certifying that you may contribute it under
  the project's license ([developercertificate.org](https://developercertificate.org)).
  `scripts/check-dco.sh` checks each pull request's commits. There is no CLA.
- Keep pull requests focused on one module or concern; CI must be green.

## Reporting security issues

Do not open public issues for vulnerabilities; follow [SECURITY.md](SECURITY.md).

## License

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
