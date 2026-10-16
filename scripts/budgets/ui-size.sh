#!/usr/bin/env bash
# UI build gates (SPEC §9.1, docs/design/ui.md §18.2 G1). The first screen
# loads index.html's scripts, their static imports and the stylesheets it
# links: JS <= 120 KB gz, CSS <= 20 KB gz. Route groups load later as their
# own chunks; every JS file together stays <= 280 KB gz. Also: no inline
# scripts or style attributes in the built HTML, no other origins.
# Run after `make ui`.
set -euo pipefail
cd "$(dirname "$0")/../.."

dist=internal/webui/dist
initial_js_budget=$((120 * 1024))
initial_css_budget=$((20 * 1024))
total_js_budget=$((280 * 1024))

if grep -q 'has not been built into this binary' "$dist/index.html"; then
	echo "ui-size: $dist holds the placeholder; run make ui first" >&2
	exit 1
fi

fail=0
# Paths in index.html are absolute; chunks import siblings as "./name.js".
attr() { grep -oE "$1=\"/[^\"]+\"" | sed -E "s/^$1=\"\\/(.*)\"$/\\1/"; }
scripts=$(grep -oiE '<script[^>]*src="[^"]*"[^>]*>' "$dist/index.html" | attr src || true)
styles=$(grep -oiE '<link[^>]*rel="stylesheet"[^>]*>' "$dist/index.html" | attr href || true)

# Static imports only: a lazy chunk is `import(`, which the pattern can't match.
initial=""
queue=$scripts
while [ -n "$queue" ]; do
	f=${queue%%$'\n'*}
	[ "$f" = "$queue" ] && queue="" || queue=${queue#*$'\n'}
	if printf '%s\n' "$initial" | grep -qxF "$f"; then continue; fi
	initial=${initial:+$initial$'\n'}$f
	case "$f" in *.js) ;; *) continue ;; esac
	[ -f "$dist/$f.gz" ] || continue
	dir=$(dirname "$f")
	for dep in $(gzip -dc "$dist/$f.gz" | grep -oE "(from|import)[[:space:]]*[\"']\./[^\"'(]+\.js[\"']" | sed -E "s/.*[\"']\.\/(.*)[\"']\$/\1/" | sort -u); do
		queue=${queue:+$queue$'\n'}$dir/$dep
	done
done

# The build embeds only the gzip variants of scripts and stylesheets, so
# those are the bytes that count.
initial_js=0
initial_css=0
echo "ui-size: first screen"
for f in $initial $styles; do
	if [ ! -f "$dist/$f.gz" ]; then
		echo "ui-size: $f is referenced but $dist/$f.gz is missing" >&2
		fail=1
		continue
	fi
	n=$(wc -c <"$dist/$f.gz" | tr -d ' ')
	printf '  %7d B gz  %s\n' "$n" "$f"
	case "$f" in
	*.css) initial_css=$((initial_css + n)) ;;
	*) initial_js=$((initial_js + n)) ;;
	esac
done

total_js=0
total_css=0
chunks=0
while IFS= read -r f; do
	n=$(wc -c <"$f" | tr -d ' ')
	case "$f" in
	*.js.gz)
		total_js=$((total_js + n))
		chunks=$((chunks + 1))
		;;
	*.css.gz) total_css=$((total_css + n)) ;;
	esac
done < <(find "$dist" -type f \( -name '*.js.gz' -o -name '*.css.gz' \))
if [ "$total_js" -eq 0 ]; then
	echo "ui-size: no *.js.gz in $dist; the precompress step did not run" >&2
	exit 1
fi
if [ "$initial_js" -eq 0 ]; then
	echo "ui-size: index.html loads no script" >&2
	exit 1
fi

echo "ui-size: every script"
find "$dist" -type f -name '*.js.gz' -exec wc -c {} + | grep -v ' total$' | sort -rn | while read -r n f; do
	f=${f#"$dist"/}
	printf '  %7d B gz  %s\n' "$n" "${f%.gz}"
done

summary=$(
	printf 'first screen js %d B gz (budget %d), css %d B gz (budget %d)\n' "$initial_js" "$initial_js_budget" "$initial_css" "$initial_css_budget"
	printf 'all chunks   js %d B gz in %d files (budget %d), css %d B gz\n' "$total_js" "$chunks" "$total_js_budget" "$total_css"
)
printf '%s\n' "$summary" | sed 's/^/ui-size: /'
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	printf '### UI size\n\n~~~\n%s\n~~~\n' "$summary" >>"$GITHUB_STEP_SUMMARY"
fi

[ "$initial_js" -le "$initial_js_budget" ] || {
	echo "ui-size: first-screen JS over budget" >&2
	fail=1
}
[ "$initial_css" -le "$initial_css_budget" ] || {
	echo "ui-size: first-screen CSS over budget" >&2
	fail=1
}
[ "$total_js" -le "$total_js_budget" ] || {
	echo "ui-size: JS over budget across all chunks" >&2
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
