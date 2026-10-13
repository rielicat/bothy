#!/usr/bin/env bash
# Compressed gateway image size (SPEC §11.3): fail above 30 MB, warn above 20.
# Two measures, both held to the budget: the whole `docker save` archive
# under gzip -9, and the registry-style sum of the gzip-compressed layers
# (what a pull downloads). BUDGET_MB / WARN_MB change the lines, e.g. to
# report the sandbox images.
#
#   scripts/budgets/image-size.sh ghcr.io/rielicat/bothy:dev
set -euo pipefail

image=${1:?usage: $0 <image>}
budget=$((${BUDGET_MB:-30} * 1024 * 1024))
warn=$((${WARN_MB:-20} * 1024 * 1024))

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
docker save -o "$tmp/image.tar" "$image"
saved=$(gzip -9 -c "$tmp/image.tar" | wc -c | tr -d ' ')

# manifest.json names the layer files in both the classic store's archive
# (uncompressed layer tars) and the containerd store's OCI layout (blobs that
# are already gzip, as a registry serves them).
mkdir "$tmp/x"
tar -xf "$tmp/image.tar" -C "$tmp/x"
layers=0
count=0
while IFS= read -r l || [ -n "$l" ]; do
	[ -n "$l" ] || continue
	f="$tmp/x/$l"
	if [ "$(od -An -tx1 -N2 "$f" | tr -d ' \n')" = 1f8b ]; then
		n=$(wc -c <"$f" | tr -d ' ')
	else
		n=$(gzip -6 -c "$f" | wc -c | tr -d ' ')
	fi
	layers=$((layers + n))
	count=$((count + 1))
done < <(sed -n 's/.*"Layers":\[\([^]]*\)\].*/\1/p' "$tmp/x/manifest.json" | tr ',' '\n' | tr -d '" ')

mib() { awk -v b="$1" 'BEGIN { printf "%.1f", b / 1048576 }'; }
echo "image-size: $image save|gzip -9 $(mib "$saved") MiB, registry layers $(mib "$layers") MiB in $count layers (budget $((budget >> 20)), warning $((warn >> 20)))"
worst=$((saved > layers ? saved : layers))
if [ "$count" -eq 0 ] || [ "$worst" -gt "$budget" ]; then
	echo "image-size: over budget (or no layers found)" >&2
	exit 1
fi
if [ "$worst" -gt "$warn" ]; then
	echo "::warning::image-size: $(mib "$worst") MiB is past the $((warn >> 20)) MB warning line"
fi
