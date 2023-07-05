include common.mk
STATIC_IMAGE=luthersystems/pushprox

STATIC_IMAGE_DUMMY=${call IMAGE_DUMMY,${STATIC_IMAGE}/${VERSION}}

HWTYPE=$(shell uname -m)
LOCALARCH=$(if $(findstring ${HWTYPE},"x86_64"),amd64,${HWTYPE})

.PHONY: default
default: static

.PHONY: static-checks
static-checks:
	./scripts/static-checks.sh

.PHONY: go-test
go-test:
	${GO_TEST_TIMEOUT_10} ./...

proxy/proxy:
	GOOS=linux go build -tags netgo -ldflags '-w -extldflags "-static"' -o ./build/proxy ./proxy

.PHONY: clean
clean:
	rm -rf build
	rm -fr proxy/proxy

.PHONY: citest
citest: static-checks go-test
	@

.PHONY: static
static: ${STATIC_IMAGE_DUMMY}
	@

.PHONY: push
push: ${FQ_STATIC_IMAGE_DUMMY}
	@

build-%: LOADARG=$(ifpushprox$(findstring $*,${LOCALARCH}),--load)
build-%: Dockerfile proxy/proxy
	${DOCKER} buildx build \
		--platform linux/$* \
		${LOADARG} \
		-t ${STATIC_IMAGE}:${VERSION} \
		.

${STATIC_IMAGE_DUMMY}:
	make build-${LOCALARCH}
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
