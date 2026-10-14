# deploy/sandbox

Tool box images (SPEC §2.6; module M5), built from the repository root with
`docker buildx build -f deploy/sandbox/Dockerfile --target base|node .`.
`bothy-sandbox` (target `base`) is Debian trixie-slim with python3, uv, bash,
git, curl, jq, ripgrep and friends, `sbx-init` at `/opt/bothy/sbx-init` and
user `sbx` (10001); `bothy-sandbox-node` (target `node`) adds Node 24 and npm.
Every base image is pinned by digest, every setuid/setgid bit is stripped, and
`gitconfig` becomes `/etc/gitconfig` (no hooks, fsmonitor or pager, no `file`
or `ext` transports). `Dockerfile.dockerignore` limits the build context to the
few paths the stages read. Releases build the same targets through goreleaser
with `SBXINIT=release`, which copies the signed release `sbx-init` binary
instead of compiling it. `test/acceptance.sh` runs the Docker (and, with
`MODE=bwrap`, bubblewrap) acceptance suites in throwaway containers;
`API_LIMIT=1.44` runs the Docker suite in the Docker 25 bind-mount mode on a
newer engine, and `PREFIX` renames every container, volume and image it
creates.
