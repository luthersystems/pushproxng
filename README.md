# README #

*DEPRECATED*: This repo is hosted for historical reasons. Pushprox was a
library to help solve the scraping problem of fetching prometheus metrics out
of chaincode. This is no longer a problem, now that we have external chaincode
which is scraped by prometheus directly.

## Build and release

The image `luthersystems/pushprox` runs the proxy on the hardened
`luthersystems/service-base-alpine` base as the user `nobody`, and runs the
build-harden kit (luthersystems/buildenv `images/harden`) last, so it trusts
no distrusted root CA.

| Command | What it does |
|---|---|
| `make citest` | golangci-lint, `go test` and govulncheck |
| `make check-harden` | Build the image for this machine and check it with the build-harden kit |

To release, run the `Release (tag)` workflow from `main` (leave `version`
empty for the next patch). It tags the release, and the tag builds, checks and
pushes the multi-arch image.
