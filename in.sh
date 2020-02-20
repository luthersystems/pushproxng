#!/usr/bin/env bash

set -o xtrace
set -o errexit
set -o nounset
set -o pipefail

[ -f /.dockerenv ]

mkdir -p /tmp/pushproxngbuild
cd /tmp/pushproxngbuild

export GOPATH=/tmp/pushproxnggopath
export GO111MODULE=on

cp -r /tmp/pushproxngsrc/. ./

go install -tags netgo -ldflags '-w -extldflags "-static"' ./...

"$GOPATH"/bin/testmetric &>./stdamp-testmetric &
"$GOPATH"/bin/proxy --listen=:8080 &>./stdamp-proxy &
sleep 0.25
"$GOPATH"/bin/client --proxy=localhost:8080 --fqdn=www.example.com --target=localhost:9100 &>./stdamp-client &
sleep 0.25
tail -f ./stdamp-* &
apk add wget
set +o errexit
for i in `seq 1 3`
do
  wget -e use_proxy=yes -e http_proxy=localhost:8080 -O- http://www.example.com/metrics
  sleep 1
done
sleep 1
cat ./stdamp-*

echo "+OK (in.sh)"
