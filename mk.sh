#!/usr/bin/env bash

set -o xtrace
set -o errexit
set -o nounset
set -o pipefail

docker run --rm -v /tmp/pushproxnggopath:/tmp/pushproxnggopath -v "$(greadlink -f .):/tmp/pushproxngsrc" --entrypoint sh golang:alpine /tmp/pushproxngsrc/in.sh
