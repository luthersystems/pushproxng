#!/usr/bin/env bash
# Run govulncheck and fail on a vulnerability the code calls, unless a live
# line of scripts/govulncheck-exemptions.txt names it (see that file for why).
#
# Usage: scripts/govulncheck.sh GOVULNCHECK_VERSION
set -euo pipefail

version=${1:?usage: govulncheck.sh GOVULNCHECK_VERSION}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
exemptions="$root/scripts/govulncheck-exemptions.txt"
today=$(date -u +%F)

command -v jq >/dev/null || { echo "govulncheck.sh: jq is required" >&2; exit 2; }

out=$(mktemp)
ids=$(mktemp)
trap 'rm -f "$out" "$ids"' EXIT
# -format json exits 0 whatever it finds; the findings are read below.
(cd "$root" && go run "golang.org/x/vuln/cmd/govulncheck@${version}" -format json ./...) >"$out"

# A finding is called when the first frame of its trace names a function.
# jq writes to a file, not a process substitution, so that a jq error
# (bad JSON) fails the script instead of reading as no findings.
jq -r 'select(.finding != null and .finding.trace[0].function != null) | .finding.osv' "$out" >"$ids"
mapfile -t called < <(sort -u "$ids")

fail=0
for id in "${called[@]}"; do
	# The expiry of the first exemption line for id, or empty. awk skips
	# comments itself: grep -v would exit 1 on a file of only comments.
	expires=$(awk -F'|' -v id="$id" '
		/^[[:space:]]*#/ { next }
		{ k = $1; e = $2; gsub(/[[:space:]]/, "", k); gsub(/[[:space:]]/, "", e) }
		k == id { print e; exit }' "$exemptions")
	if [ -n "$expires" ]; then
		if [[ ! "$expires" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
			echo "govulncheck: $id exemption has a bad expiry '$expires' (want YYYY-MM-DD)" >&2
		elif [[ ! "$expires" < "$today" ]]; then
			echo "govulncheck: $id exempt until $expires"
			continue
		else
			echo "govulncheck: $id exemption expired on $expires" >&2
		fi
	fi
	echo "govulncheck: called vulnerability $id (https://pkg.go.dev/vuln/$id)" >&2
	fail=1
done

if [ "$fail" -ne 0 ]; then
	# Print the readable report for the log, from the saved JSON.
	(cd "$root" && go run "golang.org/x/vuln/cmd/govulncheck@${version}" -mode convert <"$out") || true
	exit 1
fi
echo "govulncheck: no called vulnerabilities"
