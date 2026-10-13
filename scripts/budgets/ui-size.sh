#!/usr/bin/env bash
# UI build gates (SPEC §9, §12.2 M12): JS <= 120 KB gz, CSS <= 20 KB gz, no
# inline scripts or style attributes in the built HTML, no other origins.
# Run after `make ui`.
set -euo pipefail
cd "$(dirname "$0")/../.."

dist=internal/webui/dist
js_budget=$((120 * 1024))
css_budget=$((20 * 1024))

if grep -q 'has not been built into this binary' "$dist/index.html"; then
	echo "ui-size: $dist holds the placeholder; run make ui first" >&2
	exit 1
fi

# The build embeds only the gzip variants of scripts and stylesheets, so
# those are the bytes that count.
js=0
css=0
while IFS= read -r f; do
	n=$(wc -c <"$f" | tr -d ' ')
	case "$f" in
	*.js.gz) js=$((js + n)) ;;
	*.css.gz) css=$((css + n)) ;;
	esac
done < <(find "$dist" -type f \( -name '*.js.gz' -o -name '*.css.gz' \))
if [ "$js" -eq 0 ]; then
	echo "ui-size: no *.js.gz in $dist; the precompress step did not run" >&2
	exit 1
fi

fail=0
printf 'ui-size: js %d B gz (budget %d), css %d B gz (budget %d)\n' "$js" "$js_budget" "$css" "$css_budget"
[ "$js" -le "$js_budget" ] || {
	echo "ui-size: JS over budget" >&2
	fail=1
}
[ "$css" -le "$css_budget" ] || {
	echo "ui-size: CSS over budget" >&2
	fail=1
}

# CSP is script-src 'self'; style-src 'self' without unsafe-inline.
if grep -oiE '<script[^>]*>' "$dist/index.html" | grep -viq 'src='; then
	echo "ui-size: inline <script> in index.html" >&2
	fail=1
fi
if grep -Eiq '<style|[[:space:]]style=' "$dist/index.html"; then
	echo "ui-size: inline style in index.html" >&2
	fail=1
fi
if grep -Eiq '(src|href)="(https?:)?//' "$dist/index.html"; then
	echo "ui-size: index.html references another origin" >&2
	fail=1
fi
if find "$dist" -name '*.css.gz' -exec gzip -dc {} + | grep -Ei '(url\(|@import)[[:space:]]*["'\'']?(https?:)?//' >/dev/null; then
	echo "ui-size: CSS references another origin" >&2
	fail=1
fi
exit $fail
