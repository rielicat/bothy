#!/usr/bin/env bash
# Runs the sandbox acceptance tests (SPEC §12.2, M5) against a real Docker
# engine, the way compose deploys Bothy: sandboxd in one container holding the
# engine socket, the gateway-side test in another, both sharing the work and
# run volumes (unix sockets included). Works on Linux and on Docker Desktop,
# where the host itself cannot share those volumes.
#
#   deploy/sandbox/test/acceptance.sh                         # -tags docker suite
#   MODE=bwrap deploy/sandbox/test/acceptance.sh              # -tags bwrap suite in a container
#   MODE=runsc EXPECT=runsc|fallback|refused deploy/sandbox/test/acceptance.sh
#
# MODE=runsc needs gVisor registered with the engine: with --host-uds=open for
# EXPECT=runsc, without it for fallback (BOTHY_SBX_RUNTIME=auto) and refused
# (BOTHY_SBX_RUNTIME=runsc). Everything it creates is labelled bothy.test=1 and
# removed on exit (KEEP_IMAGES=1 keeps the images). Env: RUNTIME overrides
# BOTHY_SBX_RUNTIME; NET=1 (default) lets the test container reach example.com.
set -euo pipefail
cd "$(dirname "$0")/../../.."

MODE=${MODE:-docker}
EXPECT=${EXPECT:-runsc}
NET=${NET:-1}
TAGS=docker
TESTS=TestDockerAcceptance
DEFAULT_RUNTIME=auto
if [ "$MODE" = runsc ]; then
	TAGS=runsc
	TESTS=TestRunsc
	case "$EXPECT" in
	runsc) TESTS='TestRunsc|TestDockerAcceptance' DEFAULT_RUNTIME=runsc ;;
	refused) DEFAULT_RUNTIME=runsc ;;
	esac
fi
RUNTIME=${RUNTIME:-$DEFAULT_RUNTIME}
TAG=bothy-m5-acceptance
LABEL=bothy.test=1
BASE=docker.io/library/debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
ARCH=$(docker version --format '{{.Server.Arch}}')
OUT=$(mktemp -d)
WORK_VOL=bothy_test_work
RUN_VOL=bothy_test_run
SANDBOXD=bothy-test-sandboxd

cleanup() {
	status=$?
	if [ "$MODE" != bwrap ] && [ "$status" -ne 0 ] && docker inspect "$SANDBOXD" >/dev/null 2>&1; then
		echo "--- sandboxd logs" >&2
		docker logs "$SANDBOXD" >&2 || true
	fi
	docker stop -t 10 "$SANDBOXD" >/dev/null 2>&1 || true
	docker rm -f "$SANDBOXD" bothy-test-gateway bothy-test-bwrap >/dev/null 2>&1 || true
	# Boxes of this run only: sandboxd labels them with its run volume.
	leftovers=$(docker ps -aq --filter "label=bothy.run_volume=$RUN_VOL")
	if [ -n "$leftovers" ]; then
		# shellcheck disable=SC2086
		docker rm -f $leftovers >/dev/null || true
	fi
	docker volume rm -f "$WORK_VOL" "$RUN_VOL" >/dev/null 2>&1 || true
	if [ "${KEEP_IMAGES:-0}" != 1 ]; then
		docker image rm -f "$TAG:box" "$TAG:harness" "$TAG:bwrap" >/dev/null 2>&1 || true
	fi
	rm -rf "$OUT"
	exit "$status"
}
trap cleanup EXIT

for c in $(docker ps -aq --filter label=bothy.box=1 --filter "label=bothy.run_volume=$RUN_VOL"); do
	echo "acceptance: removing leftover box $c from an earlier run" >&2
	docker rm -f "$c" >/dev/null
done

build() { docker buildx build --load --label "$LABEL" "$@"; }

if [ "$MODE" = bwrap ]; then
	CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go test -c -tags bwrap -o "$OUT/bwrap.test" ./internal/sandbox/bwrap
	cat >"$OUT/Dockerfile" <<-EOF
		FROM $BASE
		RUN apt-get update && apt-get install -y --no-install-recommends bubblewrap python3 bash procps util-linux ca-certificates \
		 && rm -rf /var/lib/apt/lists/*
		COPY bwrap.test /opt/test/bwrap.test
	EOF
	build -t "$TAG:bwrap" "$OUT"
	# bwrap needs unprivileged user namespaces and a fresh /proc, which
	# Docker's default seccomp profile and masked /proc paths forbid.
	docker run --rm --name bothy-test-bwrap --label "$LABEL" --user 1000:1000 \
		--security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined \
		-e BOTHY_SECRET_PROBE=leak-me --entrypoint /opt/test/bwrap.test "$TAG:bwrap" -test.v -test.count=1 -test.timeout 10m
	exit 0
fi

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -o "$OUT/bothy" ./cmd/bothy
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go test -c -tags "$TAGS" -o "$OUT/sandbox.test" ./internal/sandbox
build -f deploy/sandbox/Dockerfile --target base -t "$TAG:box" .
BOX_IMAGE=$(docker image inspect "$TAG:box" --format '{{.Id}}')

# Both containers start from the gateway image layout of SPEC §2.3: named
# volumes copy the owner and mode of these directories on first mount.
cat >"$OUT/Dockerfile" <<-EOF
	FROM $BASE
	RUN install -d -m 2770 -o 65532 -g 10001 /work && install -d -m 0770 -o 65532 -g 65532 /run/bothy
	COPY bothy sandbox.test /opt/test/
EOF
build -t "$TAG:harness" "$OUT"

docker volume create --label "$LABEL" "$WORK_VOL" >/dev/null
docker volume create --label "$LABEL" "$RUN_VOL" >/dev/null
SOCK_GID=$(docker run --rm --label "$LABEL" -v /var/run/docker.sock:/var/run/docker.sock --entrypoint stat "$TAG:harness" -c %g /var/run/docker.sock)

docker run -d --name "$SANDBOXD" --label "$LABEL" \
	--network none --read-only --cap-drop ALL --security-opt no-new-privileges:true \
	--user 65532:65532 --group-add "$SOCK_GID" --group-add 65532 \
	-v /var/run/docker.sock:/var/run/docker.sock -v "$RUN_VOL:/run/bothy" \
	-e BOTHY_SBX_WORK_VOLUME="$WORK_VOL" -e BOTHY_SBX_RUN_VOLUME="$RUN_VOL" \
	-e BOTHY_SBX_IMAGES="base=$BOX_IMAGE" -e BOTHY_SBX_RUNTIME="$RUNTIME" -e BOTHY_SBX_GATEWAY_UID=65532 \
	--entrypoint /opt/test/bothy "$TAG:harness" sandboxd >/dev/null

docker run --rm --name bothy-test-gateway --label "$LABEL" \
	--cap-drop ALL --security-opt no-new-privileges:true --user 65532:65532 --group-add 10001 \
	-v "$WORK_VOL:/work" -v "$RUN_VOL:/run/bothy" \
	-e BOTHY_TEST_SANDBOXD_SOCKET=/run/bothy/sandboxd.sock -e BOTHY_TEST_WORK=/work -e BOTHY_TEST_RUN=/run/bothy \
	-e BOTHY_TEST_NET="$NET" -e BOTHY_TEST_RUNSC_EXPECT="$EXPECT" \
	--entrypoint /opt/test/sandbox.test "$TAG:harness" -test.v -test.count=1 -test.timeout 15m -test.run "$TESTS"
