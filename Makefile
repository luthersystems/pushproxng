include common.mk
STATIC_IMAGE=luthersystems/pushprox

STATIC_IMAGE_DUMMY=${call IMAGE_DUMMY,${STATIC_IMAGE}/${VERSION}}
FQ_STATIC_IMAGE=$(call FQ_DOCKER_IMAGE,${STATIC_IMAGE})
FQ_STATIC_IMAGE_DUMMY=$(call PUSH_DUMMY,${FQ_STATIC_IMAGE}/${BUILD_VERSION})
FQ_MANIFEST_DUMMY=$(call MANIFEST_DUMMY,${FQ_STATIC_IMAGE}/${BUILD_VERSION})

HWTYPE=$(shell uname -m)
LOCALARCH=$(if $(filter x86_64,${HWTYPE}),amd64,$(if $(filter aarch64 arm64,${HWTYPE}),arm64,${HWTYPE}))

.PHONY: default
default: static

.PHONY: static-checks
static-checks:
	./scripts/static-checks.sh

.PHONY: go-test
go-test:
	${GO_TEST_TIMEOUT_10} ./...

# Every input of the image build. The image dummy below depends on them,
# so an edit (committed or not) rebuilds the image for the same VERSION.
GO_SOURCES:=$(shell find . -name '*.go' -not -path './.git/*')
IMAGE_INPUTS=Dockerfile .dockerignore go.mod go.sum ${GO_SOURCES}

.PHONY: clean
clean:
	rm -rf build

# govulncheck reports only vulnerabilities the code can reach, in the Go
# standard library and the modules, for the Go in go.mod's toolchain line.
GOVULNCHECK_VERSION=v1.9.0

.PHONY: govulncheck
govulncheck:
	./scripts/govulncheck.sh ${GOVULNCHECK_VERSION}

.PHONY: citest
citest: static-checks go-test govulncheck
	@

# Check the image this checkout builds for the local architecture against
# the build-harden policies (scripts/check-harden.sh).
.PHONY: check-harden
check-harden: static
	./scripts/check-harden.sh ${STATIC_IMAGE}:${VERSION} linux/${LOCALARCH}

.PHONY: static
static: ${STATIC_IMAGE_DUMMY}
	@

.PHONY: push
push: ${FQ_STATIC_IMAGE_DUMMY}
	@

build-%: LOADARG=$(if $(findstring $*,${LOCALARCH}),--load)
build-%: ${IMAGE_INPUTS}
	${DOCKER} buildx build \
		--platform linux/$* \
		${LOADARG} \
		-t ${STATIC_IMAGE}:${VERSION} \
		.

${STATIC_IMAGE_DUMMY}: ${IMAGE_INPUTS}
	$(MAKE) build-${LOCALARCH}
	${MKDIR_P} $(dir $@)
	${TOUCH} $@

${FQ_STATIC_IMAGE_DUMMY}: ${STATIC_IMAGE_DUMMY}
	${DOCKER} tag ${STATIC_IMAGE}:${VERSION} ${FQ_STATIC_IMAGE}:${BUILD_VERSION}
	${DOCKER} push ${FQ_STATIC_IMAGE}:${BUILD_VERSION}
	${MKDIR_P} $(dir $@)
	${TOUCH} $@

.PHONY: push-manifests
push-manifests: ${FQ_MANIFEST_DUMMY}
	@

${FQ_MANIFEST_DUMMY}:
	${DOCKER} buildx imagetools create \
		--tag ${FQ_STATIC_IMAGE}:latest \
		${FQ_STATIC_IMAGE}:${VERSION}-arm64 \
		${FQ_STATIC_IMAGE}:${VERSION}-amd64
	${DOCKER} buildx imagetools create \
		--tag ${FQ_STATIC_IMAGE}:${VERSION} \
		${FQ_STATIC_IMAGE}:${VERSION}-arm64 \
		${FQ_STATIC_IMAGE}:${VERSION}-amd64
