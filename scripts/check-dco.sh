#!/usr/bin/env bash
# Fails when a commit in <base>..HEAD has no Developer Certificate of Origin
# sign-off by its author (CONTRIBUTING.md, "Commits and pull requests").
#
#   scripts/check-dco.sh origin/main
set -euo pipefail
base=${1:?usage: $0 <base ref>}

fail=0
for sha in $(git rev-list --no-merges "$base..HEAD"); do
	email=$(git log -1 --format=%ae "$sha")
	if ! git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$sha" | grep -qiF "<$email>"; then
		echo "check-dco: $(git log -1 --format='%h %s' "$sha"): no Signed-off-by for $email; amend with: git commit --amend -s" >&2
		fail=1
	fi
done
exit $fail
