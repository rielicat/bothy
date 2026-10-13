#!/usr/bin/env bash
# Checks the gateway image against SPEC §2.3: user, entrypoint, binary, the
# baked directories with their owners and modes (named volumes copy them on
# first mount, so they must be right in the image), no setuid/setgid files and
# no shell.
#
#   scripts/budgets/image-layout.sh ghcr.io/rielicat/bothy:dev
set -euo pipefail

image=${1:?usage: $0 <image>}
fail=0

cfg=$(docker image inspect -f '{{.Config.User}}|{{json .Config.Entrypoint}}|{{range .Config.Env}}{{.}} {{end}}' "$image")
IFS='|' read -r user entry env <<<"$cfg"
[ "$user" = "65532:65532" ] || {
	echo "image-layout: USER is '$user', want 65532:65532" >&2
	fail=1
}
[ "$entry" = '["/bothy"]' ] || {
	echo "image-layout: ENTRYPOINT is $entry, want [\"/bothy\"]" >&2
	fail=1
}
case " $env " in *" BOTHY_DEPLOYMENT=container "*) ;; *)
	echo "image-layout: BOTHY_DEPLOYMENT=container not set" >&2
	fail=1
	;;
esac

cid=$(docker create "$image")
trap 'docker rm -f "$cid" >/dev/null' EXIT
# GNU tar prints "mode uid/gid size ..." and bsdtar (macOS) "mode links uid
# gid size ..."; normalise both to "mode uid/gid path" (a symlink's path is
# the field before "->").
listing=$(docker export "$cid" | tar --numeric-owner -tvf - | awk '{
	owner = ($2 ~ /\//) ? $2 : $3 "/" $4
	path = $NF
	for (i = 1; i <= NF; i++) if ($i == "->") path = $(i - 1)
	print $1, owner, path
}')

want() { # mode owner path
	local line mode owner
	line=$(printf '%s\n' "$listing" | awk -v p="$3" '$NF == p || $NF == p "/"' | head -n1)
	if [ -z "$line" ]; then
		echo "image-layout: $3 missing" >&2
		fail=1
		return 0
	fi
	read -r mode owner _ <<<"$line"
	if [ "$mode" != "$1" ] || [ "$owner" != "$2" ]; then
		echo "image-layout: $3 is $mode $owner, want $1 $2" >&2
		fail=1
	fi
}
want drwx------ 65532/65532 data
want drwx------ 65532/65532 keys
want drwxrws--- 65532/10001 work
want drwxrwx--- 65532/65532 run/bothy

if ! printf '%s\n' "$listing" | awk '$NF == "bothy" && $1 ~ /^-..x/' | grep -q .; then
	echo "image-layout: /bothy missing or not executable" >&2
	fail=1
fi
shells=$(printf '%s\n' "$listing" | awk '$NF ~ /^(usr\/)?s?bin\/(sh|bash|dash|ash|zsh|busybox)$/')
if [ -n "$shells" ]; then
	echo "image-layout: the image has a shell:" >&2
	printf '%s\n' "$shells" >&2
	fail=1
fi
if printf '%s\n' "$listing" | awk '$1 ~ /^-..[sS]|^-.....[sS]/' | grep -q .; then
	echo "image-layout: setuid/setgid files present:" >&2
	printf '%s\n' "$listing" | awk '$1 ~ /^-..[sS]|^-.....[sS]/' >&2
	fail=1
fi
[ $fail -eq 0 ] && echo "image-layout: ok"
exit $fail
