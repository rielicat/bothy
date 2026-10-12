#!/usr/bin/env bash
# Fails a module branch that touches paths outside its OWNERSHIP.md entry, and
# fails if two modules claim the same tracked file.
#
#   scripts/check-ownership.sh [--module M3] [--base origin/main]
#
# The module comes from --module, $BOTHY_MODULE, or a branch named m<N>/...
# ($GITHUB_HEAD_REF in pull requests); any other branch is the lead's.
# Bash 3.2 compatible (macOS /bin/bash).
set -euo pipefail
cd "$(dirname "$0")/.."

module=${BOTHY_MODULE:-}
base=${BOTHY_BASE:-}
while [ $# -gt 0 ]; do
	case "$1" in
	--module)
		module=$2
		shift 2
		;;
	--base)
		base=$2
		shift 2
		;;
	*)
		echo "usage: $0 [--module M<N>|S] [--base <ref>]" >&2
		exit 2
		;;
	esac
done

MODS=()
PATS=()
in_block=0
while IFS= read -r line; do
	case "$line" in
	'```ownership')
		in_block=1
		continue
		;;
	'```')
		if [ $in_block -eq 1 ]; then break; fi
		continue
		;;
	esac
	[ $in_block -eq 1 ] || continue
	read -r m pat rest <<<"$line"
	[ -n "$m" ] || continue
	if [ -z "$pat" ] || [ -n "$rest" ]; then
		echo "check-ownership: bad OWNERSHIP.md line: $line" >&2
		exit 2
	fi
	MODS+=("$m")
	PATS+=("$pat")
done <OWNERSHIP.md
if [ ${#MODS[@]} -eq 0 ]; then
	echo "check-ownership: no \`\`\`ownership block in OWNERSHIP.md" >&2
	exit 2
fi

matches() {
	local f=$1 pat=$2 fs ps
	case "$pat" in
	*"/**")
		case "$f" in "${pat%"/**"}"/*) return 0 ;; esac
		return 1
		;;
	esac
	fs=${f//[!\/]/}
	ps=${pat//[!\/]/}
	[ ${#fs} -eq ${#ps} ] || return 1
	# shellcheck disable=SC2254 # $pat is a glob on purpose
	case "$f" in $pat) return 0 ;; esac
	return 1
}

# owners prints every module whose entry claims file $1 (none means the lead).
owners() {
	local f=$1 m i pos neg out=""
	for m in M1 M2 M3 M4 M5 M6 M7 M8 M9 M10 M11 M12; do
		pos=0
		neg=0
		for i in "${!MODS[@]}"; do
			[ "${MODS[$i]}" = "$m" ] || continue
			case "${PATS[$i]}" in
			!*) if matches "$f" "${PATS[$i]#!}"; then neg=1; fi ;;
			*) if matches "$f" "${PATS[$i]}"; then pos=1; fi ;;
			esac
		done
		if [ $pos -eq 1 ] && [ $neg -eq 0 ]; then out="$out $m"; fi
	done
	echo "${out# }"
}

fail=0
while IFS= read -r f; do
	o=$(owners "$f")
	case "$o" in *" "*)
		echo "check-ownership: $f is claimed by more than one module: $o" >&2
		fail=1
		;;
	esac
done < <(git ls-files)

if [ -z "$module" ]; then
	branch=${GITHUB_HEAD_REF:-${GITHUB_REF_NAME:-$(git rev-parse --abbrev-ref HEAD)}}
	case "$branch" in
	[mM][0-9]/* | [mM][0-9][0-9]/*)
		n=${branch%%/*}
		module=M${n#[mM]}
		;;
	*) module=S ;;
	esac
fi
case "$module" in
S) ;;
M1 | M2 | M3 | M4 | M5 | M6 | M7 | M8 | M9 | M10 | M11 | M12)
	if [ -z "$base" ]; then
		if git rev-parse -q --verify origin/main >/dev/null; then base=origin/main; else base=main; fi
	fi
	n=0
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		n=$((n + 1))
		o=$(owners "$f")
		if [ "$o" != "$module" ]; then
			echo "check-ownership: $module may not change $f (owner: ${o:-S, via an ICR to the lead})" >&2
			fail=1
		fi
	done < <(git diff --name-only --no-renames "$base"...HEAD)
	[ $fail -eq 0 ] && echo "check-ownership: $module, $n changed file(s) against $base"
	;;
*)
	echo "check-ownership: unknown module $module" >&2
	exit 2
	;;
esac

if [ $fail -ne 0 ]; then
	exit 1
fi
echo "check-ownership: ok"
