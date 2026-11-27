# Everything the gateway builds, tests and ships. Run from this directory.
#
# GATEWAY_VERSION is the gateway's own release version, stamped into main.Version.
# DEVSHARD_PROTOCOL_VERSION is the devshard protocol version on chain, stamped into
# devshard/types.buildStateRootProtocolVersion, which is hashed into the state-root preimage and written
# into every settlement payload. The two can differ. A binary built without the protocol version falls back
# to the plain-`go build` default and signs settlements the network computes differently, so every target
# that produces a binary passes both -- including the images -- and refuses to run without them.
# Every image is labelled with the commit it was built from (`-dirty` when the work tree has any change,
# untracked files included), the gateway version, the protocol version and the source repository.

.PHONY: help require-version require-commit build test test-race test-junit cover lint verify image release clean

GATEWAY_IMAGE ?= ghcr.io/gonka-ai/devshard-gateway-v4
GATEWAY_TAGS ?=
GATEWAY_STAGE_TAG ?= stage
PLATFORM ?= linux/amd64

# `cmd/gateway` is not its own module: the go tool resolves upwards to devshard/, and the docker build
# needs the repository root, because the image copies inference-chain/ and common/ beside devshard/.
MODULE_DIR := ../..
REPO_ROOT := ../../..
DOCKERFILE := devshard/Dockerfile
IMAGE_TARGET := gateway-runtime

GIT_AT_ROOT := $(shell git -C $(REPO_ROOT) rev-parse --git-dir >/dev/null 2>&1 && [ -z "$$(git -C $(REPO_ROOT) rev-parse --show-prefix)" ] && echo yes)
COMMIT_SHA := $(if $(GIT_AT_ROOT),$(shell git -C $(REPO_ROOT) rev-parse --verify --quiet HEAD))
COMMIT ?= $(COMMIT_SHA)$(if $(COMMIT_SHA),$(if $(strip $(shell git -C $(REPO_ROOT) status --porcelain)),-dirty))
COMMIT := $(COMMIT)
IMAGE_BUILD_ARGS = \
	--build-arg GATEWAY_VERSION=$(GATEWAY_VERSION) \
	--build-arg DEVSHARD_VERSION=$(DEVSHARD_PROTOCOL_VERSION) \
	--label org.opencontainers.image.revision=$(COMMIT) \
	--label org.opencontainers.image.version=$(GATEWAY_VERSION) \
	--label ai.gonka.devshard.protocol-version=$(DEVSHARD_PROTOCOL_VERSION) \
	--label org.opencontainers.image.source=https://github.com/gonka-ai/gonka

BINARY ?= $(MODULE_DIR)/bin/gateway
LDFLAGS := -X main.Version=$(GATEWAY_VERSION) -X devshard/types.buildStateRootProtocolVersion=$(DEVSHARD_PROTOCOL_VERSION)

GOTESTSUM ?= gotest.tools/gotestsum@v1.13.0
TEST_FORMAT ?= testname
JUNIT_REPORT ?= junit-report.xml
# The packages a coverage number is worth quoting for: the request path and the money path. Everything
# else is plumbing whose percentage would only dilute the figure.
COVER_PACKAGES ?= ./engine/...,./scheduler/...,./api/...,./registry/...,./limits/...,./escrow/...
COVER_PROFILE ?= coverage.out

help:
	@echo "build        build the gateway binary into $(BINARY); needs GATEWAY_VERSION and DEVSHARD_PROTOCOL_VERSION"
	@echo "test         every gateway package, no race detector"
	@echo "test-race    the same under -race; CI runs it on every package but scenarios, which it runs without -race"
	@echo "test-junit   test-race with a JUnit report at $(JUNIT_REPORT)"
	@echo "cover        statement coverage of the request and money paths"
	@echo "lint         golangci-lint over the gateway"
	@echo "verify       test-race and lint: the gate before a push"
	@echo "image        build $(GATEWAY_IMAGE):$(GATEWAY_STAGE_TAG) into the local daemon; needs both versions, and COMMIT outside a git checkout of this repository"
	@echo "release      build and push $(GATEWAY_IMAGE) for every tag in GATEWAY_TAGS; needs both versions and GATEWAY_TAGS, and COMMIT outside a git checkout of this repository"

# Required rather than defaulted: a binary stamped with a protocol version nobody chose signs settlements no host accepts, and one without a gateway version reports no chosen release.
require-version:
	@if [ -z "$(strip $(GATEWAY_VERSION))" ]; then \
		echo "GATEWAY_VERSION is required, e.g. GATEWAY_VERSION=4.0.1" >&2; \
		exit 1; \
	fi
	@if [ -z "$(strip $(DEVSHARD_PROTOCOL_VERSION))" ]; then \
		echo "DEVSHARD_PROTOCOL_VERSION is required, e.g. DEVSHARD_PROTOCOL_VERSION=v6" >&2; \
		exit 1; \
	fi

require-commit:
	@if [ -z "$(strip $(COMMIT))" ]; then \
		echo "COMMIT is required when the repository root is not a git checkout root, e.g. COMMIT=<sha>" >&2; \
		exit 1; \
	fi

build: require-version
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./... -count=1

test-race:
	go test ./... -race -count=1

test-junit:
	go run $(GOTESTSUM) --format $(TEST_FORMAT) --junitfile $(JUNIT_REPORT) -- ./... -race -count=1

cover:
	go test ./... -count=1 -coverpkg=$(COVER_PACKAGES) -coverprofile=$(COVER_PROFILE)
	@go tool cover -func=$(COVER_PROFILE) | tail -1

lint:
	golangci-lint run ./...

verify: test-race lint

# Loaded into the local daemon rather than pushed, so a stage image can be run before it is shared.
image: require-version require-commit
	DOCKER_BUILDKIT=1 docker build \
		--target $(IMAGE_TARGET) \
		$(IMAGE_BUILD_ARGS) \
		-t $(GATEWAY_IMAGE):$(GATEWAY_STAGE_TAG) \
		-f $(REPO_ROOT)/$(DOCKERFILE) \
		--load \
		$(REPO_ROOT)

# Tags are required rather than defaulted: an image pushed under a name nobody chose is worse than a
# build that refused to run.
release: require-version require-commit
	@if [ -z "$(strip $(GATEWAY_TAGS))" ]; then \
		echo "GATEWAY_TAGS is required, e.g. GATEWAY_TAGS='v4 v4-latest'" >&2; \
		exit 1; \
	fi
	docker buildx build \
		--platform $(PLATFORM) \
		--target $(IMAGE_TARGET) \
		$(IMAGE_BUILD_ARGS) \
		$(foreach tag,$(strip $(GATEWAY_TAGS)),-t $(GATEWAY_IMAGE):$(tag)) \
		-f $(REPO_ROOT)/$(DOCKERFILE) \
		--push \
		$(REPO_ROOT)

clean:
	rm -f $(BINARY) $(JUNIT_REPORT) $(COVER_PROFILE)
