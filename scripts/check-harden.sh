#!/usr/bin/env bash
# Check that the pushprox image holds the build-harden policies
# (luthersystems/buildenv images/harden): no distrusted root CA, such as the
# eMudhra (emSign) roots, is trusted anywhere in the image.
#
# How: the kit is copied out of the image the Dockerfile pins, so the checks
# and the policy data are the ones the build used. Then:
#   - check/dockerfile.sh: the final stage runs the kit last and no later
#     step can undo it;
#   - check/image.sh: reads the exported image on the host (no emulation is
#     needed for the other architecture).
#
# Usage: scripts/check-harden.sh IMAGE PLATFORM   (PLATFORM: linux/amd64 ...)
set -euo pipefail

image=${1:?usage: check-harden.sh IMAGE PLATFORM}
platform=${2:?usage: check-harden.sh IMAGE PLATFORM}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

kit=$(sed -n 's/^FROM \(luthersystems\/build-harden[^ ]*\) AS harden$/\1/p' "$root/Dockerfile")
[ -n "$kit" ] || { echo "check-harden.sh: no build-harden stage in Dockerfile" >&2; exit 1; }

work=$(mktemp -d)
cid=$(docker create "$kit" /nonexistent)
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
docker cp "$cid:/harden" "$work/harden"

if ! command -v openssl >/dev/null; then
	SUDO=""
	command -v sudo >/dev/null && SUDO=sudo
	$SUDO apt-get update -qq && $SUDO apt-get install -y -qq --no-install-recommends openssl >/dev/null
fi

bash "$work/harden/check/dockerfile.sh" "$root/Dockerfile"
bash "$work/harden/check/image.sh" --platform "$platform" "$image"
