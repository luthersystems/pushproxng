PROJECT_REL_DIR=./
include ${PROJECT_REL_DIR}/common.mk
DOCKER_PROJECT_DIR:=$(call DOCKER_DIR, ${PROJECT_REL_DIR})

.PHONY: default
default: all

.PHONY: all

.PHONY: static-checks
static-checks:
	./scripts/static-checks.sh

.PHONY: go-test
go-test:
	${GO_TEST_TIMEOUT_10} ./...

.PHONY: citest
citest: static-checks go-test
	@
