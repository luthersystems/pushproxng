# syntax=docker/dockerfile:1.7

# The build-harden kit (luthersystems/buildenv): the final stage runs it last.
FROM luthersystems/build-harden:v0.1.60@sha256:43e503748b19601c5e7f9107334d856cbf6b4d2e6342e9647b158599915faa0d AS harden

# Build the proxy with the Go of the pinned buildenv release, so the binary
# gets the current Go standard library security fixes.
FROM luthersystems/build-go:v0.1.60@sha256:699e3406416fd53818296f17b1398250601822bdce7911b346d4d2dc2c9c6a7c AS build
USER root
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -tags netgo \
	-ldflags '-w -extldflags "-static"' -o /out/proxy ./proxy

FROM luthersystems/service-base-alpine:v0.1.60@sha256:f48719e11346ccd84f689eadaeb9460220b8592d2c5f2e74fd09d70f06233ac0
# The base runs as nobody; the kit needs root. USER nobody is set again below.
USER root
# /usr/local/bin, not /opt: the base declares VOLUME /opt, and under Docker
# a kept anonymous volume would keep serving an old binary after an upgrade.
COPY --from=build /out/proxy /usr/local/bin/proxy

# Run the build-harden kit last. It removes the distrusted root CAs (the
# eMudhra/emSign roots) from every trust store in the image. Keep it the LAST
# step that changes files: a later package step or COPY --from can bring a
# bundle back. scripts/check-harden.sh checks this Dockerfile and the built
# image in CI on both architectures.
RUN --mount=type=bind,from=harden,source=/harden,target=/harden sh /harden/harden.sh

USER nobody
ENTRYPOINT [ "tini", "--", "/usr/local/bin/proxy" ]
CMD [ "--listen=:8080" ]
