#!/usr/bin/env bash
# Runs every footprint budget that can run here (`make budgets`). The image
# checks need Docker and an image tag in $IMAGE (`make image` builds one).
set -euo pipefail
cd "$(dirname "$0")"

fail=0
run() {
	echo "== $*"
	"$@" || fail=1
}

if grep -q 'has not been built into this binary' ../../internal/webui/dist/index.html; then
	echo "== ui-size: skipped (run make ui first)"
else
	run ./ui-size.sh
fi
run ./binary-size.sh
run ./footprint.sh
if [ -n "${IMAGE:-}" ] && docker image inspect "$IMAGE" >/dev/null 2>&1; then
	run ./image-size.sh "$IMAGE"
	run ./image-layout.sh "$IMAGE"
else
	echo "== image-size, image-layout: skipped (no Docker image \$IMAGE)"
fi
exit $fail
