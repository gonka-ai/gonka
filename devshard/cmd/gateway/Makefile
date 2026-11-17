# Everything the gateway builds, tests and ships. Run from this directory.
#
# GATEWAY_VERSION is stamped into devshard/types.buildStateRootProtocolVersion, which is hashed into the
# state-root preimage and written into every settlement payload. A binary built without it falls back to
# the plain-`go build` default and signs settlements the network computes differently, so every target
# that produces a binary passes it -- including the images -- and refuses to run without it.

.PHONY: help require-version build test test-race test-junit cover lint verify image release clean

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

BINARY ?= $(MODULE_DIR)/bin/gateway
LDFLAGS := -X main.Version=$(GATEWAY_VERSION) -X devshard/types.buildStateRootProtocolVersion=$(GATEWAY_VERSION)

GOTESTSUM ?= gotest.tools/gotestsum@v1.13.0
TEST_FORMAT ?= testname
JUNIT_REPORT ?= junit-report.xml
# The packages a coverage number is worth quoting for: the request path and the money path. Everything
# else is plumbing whose percentage would only dilute the figure.
COVER_PACKAGES ?= ./engine/...,./scheduler/...,./api/...,./registry/...,./limits/...,./escrow/...
COVER_PROFILE ?= coverage.out

help:
	@echo "build        build the gateway binary into $(BINARY)"
	@echo "test         every gateway package, no race detector"
	@echo "test-race    the same under -race; this is what CI must run"
	@echo "test-junit   test-race with a JUnit report at $(JUNIT_REPORT)"
	@echo "cover        statement coverage of the request and money paths"
	@echo "lint         golangci-lint over the gateway"
	@echo "verify       test-race and lint: the gate before a push"
	@echo "image        build $(GATEWAY_IMAGE):$(GATEWAY_STAGE_TAG) into the local daemon"
	@echo "release      build and push $(GATEWAY_IMAGE) for every tag in GATEWAY_TAGS"

# Required rather than defaulted: a binary stamped with a version nobody chose signs settlements no host
# accepts.
require-version:
	@if [ -z "$(strip $(GATEWAY_VERSION))" ]; then \
		echo "GATEWAY_VERSION is required, e.g. GATEWAY_VERSION=v6" >&2; \
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
image: require-version
	DOCKER_BUILDKIT=1 docker build \
		--target $(IMAGE_TARGET) \
		--build-arg DEVSHARD_VERSION=$(GATEWAY_VERSION) \
		-t $(GATEWAY_IMAGE):$(GATEWAY_STAGE_TAG) \
		-f $(REPO_ROOT)/$(DOCKERFILE) \
		--load \
		$(REPO_ROOT)

# Tags are required rather than defaulted: an image pushed under a name nobody chose is worse than a
# build that refused to run.
release: require-version
	@if [ -z "$(strip $(GATEWAY_TAGS))" ]; then \
		echo "GATEWAY_TAGS is required, e.g. GATEWAY_TAGS='v4 v4-latest'" >&2; \
		exit 1; \
	fi
	docker buildx build \
		--platform $(PLATFORM) \
		--target $(IMAGE_TARGET) \
		--build-arg DEVSHARD_VERSION=$(GATEWAY_VERSION) \
		$(foreach tag,$(strip $(GATEWAY_TAGS)),-t $(GATEWAY_IMAGE):$(tag)) \
		-f $(REPO_ROOT)/$(DOCKERFILE) \
		--push \
		$(REPO_ROOT)

clean:
	rm -f $(BINARY) $(JUNIT_REPORT) $(COVER_PROFILE)
