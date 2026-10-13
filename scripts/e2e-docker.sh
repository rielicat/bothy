#!/usr/bin/env bash
# Builds the gateway and sandbox images for the engine's platform and runs the
# docker-tagged e2e tests against a real compose deployment (`make
# e2e-docker`). Every image, project, volume and network carries the prefix
# (BOTHY_E2E_DOCKER_PREFIX, default bothye2e<random>) and the bothy.test label,
# so runs can share an engine with each other and with a real install. The
# images are removed afterwards unless KEEP_IMAGES=1; preset BOTHY_E2E_IMAGE
# and BOTHY_E2E_SANDBOX_IMAGES to test existing images instead.
#
#   scripts/e2e-docker.sh [go test flags, e.g. -run TestComposeShell]
set -euo pipefail
cd "$(dirname "$0")/.."

prefix=${BOTHY_E2E_DOCKER_PREFIX:-bothye2e$(od -An -tx1 -N3 /dev/urandom | tr -d ' \n')}
export BOTHY_E2E_DOCKER_PREFIX=$prefix
built=()
cleanup() {
	if [ "${KEEP_IMAGES:-0}" != 1 ] && [ ${#built[@]} -gt 0 ]; then
		docker image rm -f "${built[@]}" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

build() { # tag, then buildx flags
	local tag=$1
	shift
	docker buildx build --load --label "bothy.test=$prefix" -t "$tag" "$@" .
	built+=("$tag")
}

if [ -z "${BOTHY_E2E_IMAGE:-}" ]; then
	build "$prefix/bothy:e2e" -f deploy/Dockerfile --build-arg VERSION=e2e
	export BOTHY_E2E_IMAGE=$prefix/bothy:e2e
fi
if [ -z "${BOTHY_E2E_SANDBOX_IMAGES:-}" ]; then
	build "$prefix/bothy-sandbox:e2e" -f deploy/sandbox/Dockerfile --target base
	build "$prefix/bothy-sandbox-node:e2e" -f deploy/sandbox/Dockerfile --target node
	export BOTHY_E2E_SANDBOX_IMAGES=base=$prefix/bothy-sandbox:e2e,node=$prefix/bothy-sandbox-node:e2e
fi

go test -tags 'e2e docker' -count=1 -v -timeout 20m "$@" ./e2e/
