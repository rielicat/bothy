#!/bin/sh
# Installs a signed Bothy release.
#
#   curl -fsSLO https://raw.githubusercontent.com/bothyhq/bothy/main/deploy/install.sh
#   sh install.sh                      # binary -> ~/.local/bin/bothy
#   sh install.sh --compose ~/bothy    # also the pinned compose deployment
#
# Every download is checked against the release's checksums file, whose
# Sigstore signature must come from this repository's release workflow at the
# requested tag (cosign verify-blob). There is no update channel: rerun with a
# newer --version to upgrade.
#
# Options:
#   --version X.Y.Z      release to install (default: the latest)
#   --dir DIR            binary directory (default: $BOTHY_INSTALL_DIR or ~/.local/bin)
#   --compose DIR        also install compose.yaml, searxng/settings.yml and .env into DIR
#   --skip-signature     checksums only, when cosign is unavailable (not recommended)
set -eu

REPO=bothyhq/bothy
ISSUER=https://token.actions.githubusercontent.com

version=${BOTHY_VERSION:-}
bindir=${BOTHY_INSTALL_DIR:-$HOME/.local/bin}
compose_dir=
skip_sig=0

die() {
	echo "install.sh: $*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		version=${2#v}
		shift 2
		;;
	--dir)
		bindir=$2
		shift 2
		;;
	--compose)
		compose_dir=$2
		shift 2
		;;
	--skip-signature)
		skip_sig=1
		shift
		;;
	-h | --help)
		sed -n '2,17s/^# \{0,1\}//p' "$0"
		exit 0
		;;
	*) die "unknown option $1 (see --help)" ;;
	esac
done

command -v curl >/dev/null || die "curl is required"
command -v tar >/dev/null || die "tar is required"
if command -v sha256sum >/dev/null; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "sha256sum or shasum is required"
fi
if [ $skip_sig -eq 0 ] && ! command -v cosign >/dev/null; then
	die "cosign is required to verify the release signature (https://docs.sigstore.dev/cosign/system_config/installation/); --skip-signature checks checksums only"
fi

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS $(uname -s); download a release archive from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac

if [ -z "$version" ]; then
	version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1)
	[ -n "$version" ] || die "could not determine the latest release; pass --version"
fi
base="https://github.com/$REPO/releases/download/v$version"
identity="https://github.com/$REPO/.github/workflows/release.yml@refs/tags/v$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

fetch() {
	curl -fsSL --proto '=https' --tlsv1.2 -o "$tmp/$1" "$base/$1" || die "download failed: $base/$1"
}

# verify_sums <checksums file> <file>...
verify_sums() {
	sums=$1
	shift
	fetch "$sums"
	if [ $skip_sig -eq 0 ]; then
		fetch "$sums.sigstore.json"
		cosign verify-blob --bundle "$tmp/$sums.sigstore.json" \
			--certificate-identity "$identity" --certificate-oidc-issuer "$ISSUER" \
			"$tmp/$sums" >"$tmp/cosign.log" 2>&1 || {
			cat "$tmp/cosign.log" >&2
			die "signature check failed for $sums (want a signature by $identity)"
		}
	else
		echo "install.sh: WARNING: skipping the signature check of $sums" >&2
	fi
	for f in "$@"; do
		want=$(awk -v f="$f" '$2 == f || $2 == "*" f { print $1 }' "$tmp/$sums")
		[ -n "$want" ] || die "$f is not listed in $sums"
		[ "$(sha256 "$tmp/$f")" = "$want" ] || die "checksum mismatch for $f"
	done
}

archive="bothy_${version}_${os}_${arch}.tar.gz"
echo "Installing Bothy $version ($os/$arch) into $bindir"
fetch "$archive"
verify_sums checksums.txt "$archive"
tar -xzf "$tmp/$archive" -C "$tmp" bothy
mkdir -p "$bindir"
install -m 0755 "$tmp/bothy" "$bindir/bothy" 2>/dev/null || {
	cp "$tmp/bothy" "$bindir/bothy.new" && chmod 0755 "$bindir/bothy.new" && mv "$bindir/bothy.new" "$bindir/bothy"
}
echo "Installed $bindir/bothy"
case ":$PATH:" in
*":$bindir:"*) ;;
*) echo "Add $bindir to your PATH, e.g.: export PATH=\"$bindir:\$PATH\"" ;;
esac

if [ -n "$compose_dir" ]; then
	echo "Installing the compose deployment into $compose_dir"
	fetch compose.yaml
	fetch searxng-settings.yml
	fetch env.example
	verify_sums deploy-checksums.txt compose.yaml searxng-settings.yml env.example
	mkdir -p "$compose_dir/searxng"
	cp "$tmp/compose.yaml" "$compose_dir/compose.yaml"
	cp "$tmp/searxng-settings.yml" "$compose_dir/searxng/settings.yml"
	if [ -e "$compose_dir/.env" ]; then
		echo "Kept the existing $compose_dir/.env"
	else
		secret=$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')
		gid=999
		if [ -S /var/run/docker.sock ]; then
			gid=$(stat -c %g /var/run/docker.sock 2>/dev/null || stat -f %g /var/run/docker.sock)
		fi
		(umask 077 && sed -e "s/^DOCKER_GID=.*/DOCKER_GID=$gid/" -e "s/^SEARXNG_SECRET=.*/SEARXNG_SECRET=$secret/" \
			"$tmp/env.example" >"$compose_dir/.env")
		echo "Wrote $compose_dir/.env (DOCKER_GID=$gid, random SEARXNG_SECRET)"
	fi
	echo "Start it with: cd $compose_dir && docker compose up -d && docker compose logs bothy | grep setup"
fi
