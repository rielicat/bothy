#!/usr/bin/env bash
# Compressed gateway image size (SPEC §11.3): fail above 30 MB, warn above 20.
#
#   scripts/budgets/image-size.sh ghcr.io/rielicat/bothy:dev
set -euo pipefail

image=${1:?usage: $0 <image>}
bytes=$(docker save "$image" | gzip -9 | wc -c | tr -d ' ')
mb=$(awk -v b="$bytes" 'BEGIN { printf "%.1f", b / 1048576 }')
echo "image-size: $image $mb MiB compressed (budget 30, warning 20)"
if [ "$bytes" -gt $((30 * 1024 * 1024)) ]; then
	echo "image-size: over budget" >&2
	exit 1
fi
if [ "$bytes" -gt $((20 * 1024 * 1024)) ]; then
	echo "::warning::image-size: $mb MiB is past the 20 MB warning line"
fi
