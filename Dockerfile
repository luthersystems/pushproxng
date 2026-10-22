# syntax=docker/dockerfile:1.7

# The build-harden kit (luthersystems/buildenv): the final stage runs it last.
FROM luthersystems/build-harden:v0.1.45@sha256:ed783a0cd4faf6cd791b844970f00309cec76c4ab90b1e111d641ba44d67ba46 AS harden

# Build the proxy with the Go of the pinned buildenv release, so the binary
# gets the current Go standard library security fixes.
FROM luthersystems/build-go:v0.1.45@sha256:f3628ebd1f36eb2c91fb91818c98c4657cd7eb79eaceef509b8530ab5642fe88 AS build
USER root
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -tags netgo \
	-ldflags '-w -extldflags "-static"' -o /out/proxy ./proxy

FROM luthersystems/service-base-alpine:v0.1.45@sha256:45e7d2a2f7cf115872862ece632c2057993ebff4f72c696e0bf18d77db9dece3
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
