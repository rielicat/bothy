#!/usr/bin/env bash
# Renders the compose bundle attached to a release: deploy/compose.yaml with
# every image pinned by digest, the SearXNG settings and the .env template,
# plus deploy-checksums.txt for deploy/install.sh --compose. The release
# workflow signs the checksums file.
#
#   scripts/release/compose-bundle.sh 0.1.0 dist/deploy
set -euo pipefail
cd "$(dirname "$0")/../.."

version=${1:?usage: $0 <version> <out dir>}
out=${2:?usage: $0 <version> <out dir>}

# Third-party image, pinned here rather than resolved at release time.
SEARXNG=docker.io/searxng/searxng:2026.9.25-12f8b6515@sha256:5286edb35782454ab8a102c5eff6b54bff745853191b46aeead95f225aa6dfb6

digest() {
	local d
	d=$(docker buildx imagetools inspect "$1" --format '{{.Manifest.Digest}}')
	case "$d" in sha256:*) echo "$d" ;; *)
		echo "compose-bundle: no digest for $1" >&2
		exit 1
		;;
	esac
}

gw=$(digest "ghcr.io/bothyhq/bothy:$version")
base=$(digest "ghcr.io/bothyhq/bothy-sandbox:$version")
node=$(digest "ghcr.io/bothyhq/bothy-sandbox-node:$version")

mkdir -p "$out"
sed -E \
	-e "s#ghcr\.io/bothyhq/bothy:[^@\"]+@sha256:<digest>#ghcr.io/bothyhq/bothy:$version@$gw#g" \
	-e "s#ghcr\.io/bothyhq/bothy-sandbox:[^@\"]+@sha256:<d1>#ghcr.io/bothyhq/bothy-sandbox:$version@$base#g" \
	-e "s#ghcr\.io/bothyhq/bothy-sandbox-node:[^@\"]+@sha256:<d2>#ghcr.io/bothyhq/bothy-sandbox-node:$version@$node#g" \
	-e "s#docker\.io/searxng/searxng@sha256:<digest>#$SEARXNG#g" \
	deploy/compose.yaml >"$out/compose.yaml"
if grep -n 'sha256:<' "$out/compose.yaml"; then
	echo "compose-bundle: unresolved digest placeholders above" >&2
	exit 1
fi
cp deploy/searxng/settings.yml "$out/searxng-settings.yml"
cp deploy/.env.example "$out/env.example"
(cd "$out" && sha256sum compose.yaml searxng-settings.yml env.example >deploy-checksums.txt)
echo "compose-bundle: wrote $out"
