#!/usr/bin/env bash
# Cold start to /healthz <= 500 ms and idle RSS <= 60 MB (SPEC §11.3), measured
# on the real binary by the e2e harness. BOTHY_RSS_WAIT shortens the 10 minute
# idle wait for local runs (e.g. BOTHY_RSS_WAIT=30s).
set -euo pipefail
cd "$(dirname "$0")/../.."

go test -tags 'e2e budgets' -count=1 -v -timeout 30m -run 'TestColdStart|TestIdleRSS' ./e2e/
