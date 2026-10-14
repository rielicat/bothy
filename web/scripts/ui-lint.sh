#!/usr/bin/env bash
# The class and markup rules of docs/design/ui.md §18.2 G5, over every source
# file in web/src. Part of `npm run check`.
set -euo pipefail
cd "$(dirname "$0")/../src"

# Tests name the banned patterns to assert they are absent.
files=()
while IFS= read -r f; do files+=("$f"); done < <(find . -type f \( -name '*.svelte' -o -name '*.ts' -o -name '*.css' \) ! -name '*.test.ts' ! -path './test/*' | sort)

fail=0
rule() {
	local what=$1 re=$2 hits
	if hits=$(grep -nE -- "$re" "${files[@]}"); then
		printf 'ui-lint: %s\n%s\n' "$what" "$hits" >&2
		fail=1
	fi
}

# A class token: preceded by the start of a class list, a quote, a backtick,
# a brace, whitespace or a variant colon, and followed by the same or a slash.
b='(^|[[:space:]"'\''`{:])'
e='([[:space:]"'\''`}/]|$)'

rule 'chips and pills (§2.1)' "${b}chip(-[a-z]+)?${e}"
rule 'backdrop blur (§3.4)' 'backdrop-blur|backdrop-filter'
rule 'radii above 8 px (§3.4)' "${b}rounded(-[trblse]{1,2})?-(xl|2xl|3xl)${e}"
rule 'bare rounded; use rounded-sm, -md, -lg or -full (§3.4)' "${b}rounded${e}"
rule 'shadows outside floating layers (§3.4)' "${b}shadow(-(xs|sm|md|lg|xl|2xl))?${e}"
rule 'uppercase labels (§3.2)' "${b}uppercase${e}"
rule 'letter-spaced labels (§3.2)' "${b}tracking-(wide|wider|widest)${e}"
rule 'bold weight; use 500 or 600 (§3.2)' "${b}font-(bold|extrabold|black)${e}"
rule 'pixel font sizes; use the type scale (§3.2)' 'text-\[[0-9.]+px\]'
rule 'style attributes; CSP has no unsafe-inline (§15.1)' '[[:space:]]style="'
rule 'v0.1 colour names (§3.1)' "${b}[a-z:-]*(bg|text|border|divide|ring|outline|fill|stroke|decoration|placeholder|from|via|to)-(fg|sunken|line|line-strong|ok|ok-soft|info|info-soft|warn|warn-soft|bad|bad-soft|accent-soft|risk-[a-z]+|r[1-6])(/[0-9]+)?${e}"
rule 'Tailwind palette colours (§3.1)' "${b}[a-z:-]*(bg|text|border)-(red|green|blue|amber|yellow|emerald|slate|gray|zinc|white|black)(-[0-9]{2,3})?(/[0-9]+)?${e}"

exit $fail
