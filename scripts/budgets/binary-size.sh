#!/usr/bin/env bash
# Stripped linux/amd64 binary size (SPEC §11.3: ~23 MB, ~9 MB gzip). The
# gateway image is this binary plus ~1 MB of distroless, so the gzip size is
# held to the image budget (fail above 29 MB, warn above 19 MB) for machines
# without Docker; scripts/budgets/image-size.sh measures the real image.
set -euo pipefail
cd "$(dirname "$0")/../.."

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$out/bothy" ./cmd/bothy
raw=$(wc -c <"$out/bothy" | tr -d ' ')
gz=$(gzip -9 -c "$out/bothy" | wc -c | tr -d ' ')
mib() { awk -v b="$1" 'BEGIN { printf "%.1f MiB", b / 1048576 }'; }
echo "binary-size: linux/amd64 $(mib "$raw"), gzip $(mib "$gz")"
if [ "$gz" -gt $((29 * 1024 * 1024)) ]; then
	echo "binary-size: gzip size over the 30 MB image budget" >&2
	exit 1
fi
if [ "$gz" -gt $((19 * 1024 * 1024)) ]; then
	echo "::warning::binary-size: gzip $(mib "$gz") is past the 20 MB image warning line"
fi
