#!/usr/bin/env bash
# Enforces the import rules of SPEC §3 (CONTRIBUTING.md has the summary).
# Bash 3.2 compatible (macOS /bin/bash): no associative arrays.
set -euo pipefail
cd "$(dirname "$0")/.."

MOD=github.com/bothyhq/bothy
fail=0
err() {
	echo "check-imports: $*" >&2
	fail=1
}

# owner maps a repo-relative package dir to its SPEC §12.2 module, or to the
# skeleton package it is.
owner() {
	case "$1" in
	internal/store | internal/store/* | internal/vault | internal/vault/* | internal/audit | internal/audit/*) echo M1 ;;
	internal/provider | internal/provider/*) echo M2 ;;
	internal/agent | internal/agent/* | internal/budget | internal/budget/*) echo M3 ;;
	internal/policy | internal/policy/* | internal/tools | internal/tools/*) echo M4 ;;
	internal/sandbox | internal/sandbox/* | internal/sandboxd | internal/sandboxd/* | internal/sbxinit | internal/sbxinit/*) echo M5 ;;
	internal/egress | internal/egress/*) echo M6 ;;
	internal/mcp | internal/mcp/* | internal/skills | internal/skills/*) echo M7 ;;
	internal/memory | internal/memory/* | internal/scheduler | internal/scheduler/*) echo M8 ;;
	internal/channel/discord | internal/channel/discord/* | internal/channel/slack | internal/channel/slack/*) echo M10 ;;
	internal/channel | internal/channel/telegram | internal/channel/telegram/*) echo M9 ;;
	internal/httpapi/dto) echo dto ;;
	internal/auth | internal/auth/* | internal/httpapi | internal/httpapi/* | internal/doctor | internal/doctor/* | internal/webui | internal/webui/*) echo M11 ;;
	internal/core) echo core ;;
	internal/config) echo config ;;
	internal/events) echo events ;;
	internal/deps) echo deps ;;
	internal/testkit | internal/testkit/*) echo testkit ;;
	cmd/bothy) echo cmd ;;
	cmd/sbx-init) echo sbx-init ;;
	e2e | e2e/*) echo e2e ;;
	*) echo unknown ;;
	esac
}

# thirdparty_ok reports whether package dir $1 (owner $2) may import the
# non-stdlib, non-module path $3 (SPEC §11.1 "Used by").
thirdparty_ok() {
	case "$3" in
	github.com/anthropics/* | github.com/openai/* | github.com/sashabaranov/* | google.golang.org/genai | google.golang.org/genai/* | \
		github.com/google/generative-ai-go/* | github.com/ollama/ollama/* | github.com/tmc/langchaingo/* | cloud.google.com/go/vertexai/*)
		return 1
		;; # provider SDKs: hand-written clients only (D3)
	esac
	case "$2" in
	cmd) return 0 ;;
	config | e2e) case "$3" in go.yaml.in/yaml/v3*) return 0 ;; esac ;;
	# conformance.Tool compiles tool schemas with the validator M4 uses.
	testkit) case "$3" in go.yaml.in/yaml/v3* | github.com/google/jsonschema-go/*) return 0 ;; esac ;;
	M1) case "$3" in modernc.org/sqlite* | modernc.org/libc* | golang.org/x/crypto/*) return 0 ;; esac ;;
	M4) case "$3" in github.com/google/jsonschema-go/* | golang.org/x/net/*) return 0 ;; esac ;;
	M5) case "$1:$3" in internal/sandbox/bwrap*:github.com/elastic/go-seccomp-bpf* | internal/sandbox/bwrap*:golang.org/x/net/*) return 0 ;; esac ;;
	M6) case "$3" in golang.org/x/net/*) return 0 ;; esac ;;
	M7) case "$3" in github.com/modelcontextprotocol/go-sdk/* | github.com/google/jsonschema-go/* | go.yaml.in/yaml/v3*) return 0 ;; esac ;;
	M8) case "$3" in github.com/adhocore/gronx*) return 0 ;; esac ;;
	# gorilla/websocket: disgo and slack-go take a *websocket.Dialer and report *websocket.CloseError.
	M10) case "$1:$3" in internal/channel/discord*:github.com/disgoorg/* | internal/channel/slack*:github.com/slack-go/slack* | \
		internal/channel/discord*:github.com/gorilla/websocket | internal/channel/slack*:github.com/gorilla/websocket) return 0 ;; esac ;;
	M11) case "$3" in golang.org/x/crypto/*) return 0 ;; esac ;;
	esac
	return 1
}

# internal_ok reports whether package dir $1 may import module package dir $2;
# $3 is "prod" or "test".
internal_ok() {
	local p=$1 q=$2 kind=$3 po qo
	po=$(owner "$p")
	qo=$(owner "$q")
	case "$po" in
	cmd | deps) return 0 ;;
	sbx-init) [ "$q" = internal/sbxinit ] && return 0 || return 1 ;;
	core) return 1 ;;
	e2e)
		case "$qo:$q" in
		core:* | config:* | dto:* | testkit:* | *:e2e/* | *test) return 0 ;;
		esac
		return 1
		;;
	esac
	[ "$qo" = core ] && return 0
	case "$po" in
	events | dto)
		[ "$qo" = config ] && return 0
		return 1
		;;
	esac
	[ "$qo" = config ] && return 0
	if [ "$kind" = test ]; then
		case "$qo" in testkit | events) return 0 ;; esac
	fi
	case "$po" in
	config) return 1 ;;
	testkit)
		case "$qo" in testkit | events) return 0 ;; esac
		return 1
		;;
	esac
	case "$q" in "$p"/*) return 0 ;; esac # own subpackage
	case "$p:$q" in
	internal/channel/*:internal/channel) return 1 ;; # adapters never import the hub
	internal/httpapi*:internal/httpapi/dto) return 0 ;;
	esac
	case "$po" in M*) [ "$po" = "$qo" ] && return 0 ;; esac
	return 1
}

stdlib() {
	case "${1%%/*}" in *.*) return 1 ;; esac
	return 0
}

seen=""
check_pkg() {
	local pkg=$1 prod=$2 test=$3 p q kind imp
	p=${pkg#"$MOD"/}
	case " $seen " in *" $p|$prod|$test "*) return 0 ;; esac
	seen="$seen $p|$prod|$test"
	if [ "$(owner "$p")" = unknown ]; then
		err "$p: package outside the SPEC §3 layout (add it to scripts/check-imports.sh)"
		return
	fi
	for kind in prod test; do
		if [ $kind = prod ]; then imps=$prod; else imps=$test; fi
		for imp in $imps; do
			case "$imp" in
			unsafe)
				[ "$(owner "$p")" = deps ] || err "$p imports unsafe (only internal/deps may)"
				;;
			os/exec)
				if [ $kind = prod ]; then
					case "$p" in internal/sandbox/bwrap | internal/sandbox/bwrap/* | e2e | e2e/*) ;; *) err "$p imports os/exec (only internal/sandbox/bwrap may)" ;; esac
				fi
				;;
			"$MOD"/*)
				q=${imp#"$MOD"/}
				internal_ok "$p" "$q" $kind || err "$p ($kind) imports $q"
				;;
			*)
				if ! stdlib "$imp"; then
					thirdparty_ok "$p" "$(owner "$p")" "$imp" || err "$p ($kind) imports $imp (not approved for $(owner "$p") in SPEC §11.1)"
				fi
				;;
			esac
		done
	done
}

for goos in linux darwin windows; do
	while IFS='|' read -r pkg prod test; do
		if [ -n "$pkg" ]; then check_pkg "$pkg" "$prod" "$test"; fi
	done < <(GOOS=$goos CGO_ENABLED=0 go list -e -tags 'docker runsc e2e budgets' \
		-f '{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}} {{join .XTestImports " "}}' ./...)
done

# Text rules: grep, because they are about identifiers rather than imports.
gogrep() { grep -rnE --include='*.go' --exclude='*_test.go' "$@" || true; }

hits=$(gogrep 'SystemViewer' internal/httpapi)
[ -z "$hits" ] || err "core.SystemViewer is forbidden in internal/httpapi (handlers pass the actor's Viewer):
$hits"

hits=$(gogrep 'http\.(DefaultClient|Get|Head|Post|PostForm)([^A-Za-z0-9_]|$)' internal)
[ -z "$hits" ] || err "net/http default client used in internal/ (clients come from core.Egress):
$hits"

if [ $fail -ne 0 ]; then
	exit 1
fi
echo "check-imports: ok"
