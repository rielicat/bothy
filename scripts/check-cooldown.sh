#!/usr/bin/env bash
# 14-day dependency cooldown (SPEC §10, row 12) for go.mod, tools/go.mod and
# web/package-lock.json.
#
#   scripts/check-cooldown.sh -base origin/main   # versions changed since main (CI)
#   scripts/check-cooldown.sh                     # every pinned version
set -euo pipefail
cd "$(dirname "$0")/.."
exec go -C tools run ./cooldown -root .. "$@"
