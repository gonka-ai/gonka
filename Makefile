.PHONY: release decentralized-api-release inference-chain-release tmkms-release proxy-release proxy-ssl-release bridge-release versiond-release check-docker build-testermint run-blockchain-tests test-blockchain local-build api-local-build node-local-build api-test node-test mock-server-build-docker proxy-build-docker proxy-ssl-build-docker bridge-build-docker run-bls-tests devshardctl-build devshardd-build devshardd-release print-devshard-version print-devshard-protocol-version versiond-build-docker testapp-server-build-docker

# For binary release: default linux/amd64 before local Docker defaults.
DEVSHARDD_RELEASE_DOCKER_PLATFORM := $(if $(DOCKER_PLATFORM),$(DOCKER_PLATFORM),linux/amd64)
DEVSHARDD_RELEASE_DOCKER_GOOS := $(if $(DOCKER_GOOS),$(DOCKER_GOOS),linux)
DEVSHARDD_RELEASE_DOCKER_GOARCH := $(if $(DOCKER_GOARCH),$(DOCKER_GOARCH),$(if $(filter linux/arm64,$(DEVSHARDD_RELEASE_DOCKER_PLATFORM)),arm64,amd64))

include scripts/blst-portable.mk

VERSION ?= $(shell git describe --always)
# devshardd link stamp; Testermint VERSIOND_FORCE follows this via build/devshard-version or `make print-devshard-version`.
DEVSHARD_VERSION ?= dev
# State-root / settlement protocol tag (not versiond runtime name). See devshard/docs/protocol-version.md.
DEVSHARD_PROTOCOL_VERSION ?= v2

print-devshard-version:
	@echo $(DEVSHARD_VERSION)

print-devshard-protocol-version:
	@echo $(DEVSHARD_PROTOCOL_VERSION)
TAG_NAME := "release/v$(VERSION)"
USE_REGISTRY_CACHE ?= 0
GHCR_CACHE_NAMESPACE ?= gonka-ai
PLATFORM ?= linux/amd64
GOOS ?= linux
GOARCH ?= amd64
DEVSHARDD_RELEASE_DIR ?= build/devshardd-release
ifeq ($(USE_REGISTRY_CACHE),1)
_MOCK_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/mock-server:buildcache --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/mock-server:buildcache,mode=min
_MOCK_BUILD_CMD := docker buildx build --load $(_MOCK_CACHE_ARGS)
_DEVSHARDD_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/devshardd:buildcache --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/devshardd:buildcache,mode=min
_DEVSHARDD_BUILD_CMD := docker buildx build --load $(_DEVSHARDD_CACHE_ARGS)
else
_MOCK_CACHE_ARGS :=
_MOCK_BUILD_CMD := DOCKER_BUILDKIT=1 docker build
_DEVSHARDD_CACHE_ARGS :=
_DEVSHARDD_BUILD_CMD := DOCKER_BUILDKIT=1 docker build
endif

all: build-docker

build-docker: api-build-docker node-build-docker mock-server-build-docker proxy-build-docker proxy-ssl-build-docker bridge-build-docker versiond-build-docker testapp-server-build-docker

api-build-docker:
	@make -C decentralized-api build-docker SET_LATEST=1 \
		BLST_PORTABLE=$(BLST_PORTABLE) \
		DOCKER_PLATFORM=$(DOCKER_PLATFORM) DOCKER_GOOS=$(DOCKER_GOOS) DOCKER_GOARCH=$(DOCKER_GOARCH)

node-build-docker:
	@make -C inference-chain build-docker SET_LATEST=1 \
		BLST_PORTABLE=$(BLST_PORTABLE) \
		DOCKER_PLATFORM=$(DOCKER_PLATFORM) DOCKER_GOOS=$(DOCKER_GOOS) DOCKER_GOARCH=$(DOCKER_GOARCH) \
		$(if $(GENESIS_OVERRIDES_FILE),GENESIS_OVERRIDES_FILE=$(GENESIS_OVERRIDES_FILE),)

mock-server-build-docker:
	@echo "Building mock-server JAR file..."
	@cd testermint/mock_server && ./gradlew clean && ./gradlew shadowJar
	@echo "Building mock-server docker image..."
	@$(_MOCK_BUILD_CMD) --platform $(DOCKER_PLATFORM) -t inference-mock-server -f testermint/Dockerfile testermint

proxy-build-docker:
	@make -C proxy build-docker SET_LATEST=1

proxy-ssl-build-docker:
	@make -C proxy-ssl build-docker SET_LATEST=1

bridge-build-docker:
	@make -C bridge build-docker SET_LATEST=1

versiond-build-docker:
	@echo "Building versiond docker image ($(DOCKER_PLATFORM), matches devshardd-build)..."
	@docker build --platform $(DOCKER_PLATFORM) -t versiond:latest -f versioned/Dockerfile versioned

testapp-server-build-docker:
	@echo "Building testapp-server docker image ($(DOCKER_PLATFORM))..."
	@docker build --platform $(DOCKER_PLATFORM) -t testapp-server:latest -f local-test-net/Dockerfile.testapp-server .

release: decentralized-api-release inference-chain-release tmkms-release proxy-release proxy-ssl-release bridge-release versiond-release
	@git tag $(TAG_NAME)
	@git push origin $(TAG_NAME)

decentralized-api-release:
	@echo "Releasing decentralized-api..."
	@make -C decentralized-api release
	@make -C decentralized-api docker-push

inference-chain-release:
	@echo "Releasing inference-chain..."
	@make -C inference-chain release
	@make -C inference-chain docker-push

tmkms-release:
	@echo "Releasing tmkms..."
	@make -C tmkms release
	@make -C tmkms docker-push

proxy-release:
	@echo "Releasing proxy..."
	@make -C proxy release

proxy-ssl-release:
	@echo "Releasing proxy-ssl..."
	@make -C proxy-ssl release

bridge-release:
	@echo "Releasing bridge..."
	@make -C bridge release
	@make -C bridge docker-push

versiond-release:
	@echo "Releasing versiond..."
	@make -C versioned release
	@make -C versioned docker-push

check-docker:
	@docker info > /dev/null 2>&1 || (echo "Docker Desktop is not running. Please start Docker Desktop." && exit 1)

# Default to running all tests if TESTS is not specified
TESTS ?= ALL

run-tests:
	@cd testermint && if [ "$(TESTS)" = "ALL" ]; then \
		./gradlew :test -DexcludeTags=unstable,exclude; \
	else \
		./gradlew :test --tests "$(TESTS)" -DexcludeTags=unstable,exclude; \
	fi

run-sanity: build-docker
	@cd testermint && ./gradlew :test --tests "$(TESTS)" -DincludeTags=sanity

run-bls-tests: check-docker
	@echo "Running BLS DKG integration tests (requires Docker)..."
	@cd testermint && ./gradlew test --tests "BLSDKGSuccessTest"

test-blockchain: check-docker run-blockchain-tests

# Local build targets
api-local-build:
	@echo "Building decentralized-api locally..."
	@cd decentralized-api && go build -mod=mod -o ./build/dapi

DEVSHARD_PROTOCOL_LDFLAGS = -X devshard/types.buildStateRootProtocolVersion=$(DEVSHARD_PROTOCOL_VERSION)

devshardctl-build:
	@echo "Building devshardctl (DEVSHARD_PROTOCOL_VERSION=$(DEVSHARD_PROTOCOL_VERSION))..."
	@cd devshard && go build -ldflags "-X main.Version=$(DEVSHARD_VERSION) $(DEVSHARD_PROTOCOL_LDFLAGS)" -o ../build/devshardctl ./cmd/devshardctl/

devshardd-build:
	@echo "Building devshardd (DEVSHARD_VERSION=$(DEVSHARD_VERSION) DEVSHARD_PROTOCOL_VERSION=$(DEVSHARD_PROTOCOL_VERSION))..."
	@mkdir -p build
	@$(_DEVSHARDD_BUILD_CMD) --platform $(DOCKER_PLATFORM) --target builder \
		--build-arg GOOS=$(DOCKER_GOOS) \
		--build-arg GOARCH=$(DOCKER_GOARCH) \
		--build-arg BLST_PORTABLE=1 \
		--build-arg DEVSHARD_VERSION=$(DEVSHARD_VERSION) \
		--build-arg DEVSHARD_PROTOCOL_VERSION=$(DEVSHARD_PROTOCOL_VERSION) \
		-f decentralized-api/Dockerfile . \
		-t devshardd-builder:latest -q >/dev/null
	@CID=$$(docker create devshardd-builder:latest) && \
		docker cp $$CID:/app/decentralized-api/build/devshardd build/devshardd && \
		docker rm $$CID >/dev/null
	@chmod +x build/devshardd
	@echo "$(DEVSHARD_VERSION)" > build/devshard-version
	@echo "$(DEVSHARD_PROTOCOL_VERSION)" > build/devshard-protocol-version
	@echo "Built build/devshardd ($$(file build/devshardd | grep -o 'statically linked\|dynamically linked'))"

devshardd-release:
	@$(MAKE) devshardd-build \
		DEVSHARD_VERSION=$(DEVSHARD_VERSION) \
		DEVSHARD_PROTOCOL_VERSION=$(DEVSHARD_PROTOCOL_VERSION) \
		DOCKER_PLATFORM=$(DEVSHARDD_RELEASE_DOCKER_PLATFORM) \
		DOCKER_GOOS=$(DEVSHARDD_RELEASE_DOCKER_GOOS) \
		DOCKER_GOARCH=$(DEVSHARDD_RELEASE_DOCKER_GOARCH)
	@set -e; \
		release_dir="$(DEVSHARDD_RELEASE_DIR)"; \
		if [ -z "$$release_dir" ] || [ "$$release_dir" = "/" ]; then \
			echo "Refusing to clean unsafe DEVSHARDD_RELEASE_DIR='$$release_dir'"; \
			exit 1; \
		fi; \
		rm -rf "$$release_dir"; \
		mkdir -p "$$release_dir/stage"; \
		cp build/devshardd "$$release_dir/stage/devshardd"; \
		chmod 0755 "$$release_dir/stage/devshardd"; \
		(cd "$$release_dir/stage" && zip -X -q ../devshardd.zip devshardd); \
		rm -rf "$$release_dir/stage"; \
		shasum -a 256 "$$release_dir/devshardd.zip" | awk '{print $$1}' > "$$release_dir/devshardd.zip.sha256"; \
		echo "Built $$release_dir/devshardd.zip"; \
		echo "SHA256: $$(cat "$$release_dir/devshardd.zip.sha256")"

node-local-build:
	@echo "Building inference-chain locally..."
	@make -C inference-chain build

api-test:
	@echo "Running decentralized-api tests..."
	@cd decentralized-api && go test ./... -v -short > ../api-test-output.log
	@echo "----------------------------------------"
	@echo "DECENTRALIZED-API TEST SUMMARY:"
	@PASS_COUNT=$$(grep -c "PASS:" api-test-output.log); \
	FAIL_COUNT=$$(grep -c "FAIL:" api-test-output.log); \
	NO_TEST_COUNT=$$(grep -c "no test files" api-test-output.log); \
	echo "Passed: $$PASS_COUNT tests"; \
	echo "Failed: $$FAIL_COUNT tests"; \
	echo "No test files: $$NO_TEST_COUNT packages";
	@echo "----------------------------------------"
	@if [ $$(grep -c "FAIL:" api-test-output.log) -gt 0 ]; then \
		echo "Failed tests:"; \
		grep -A 1 "FAIL:" api-test-output.log | grep -v "^\--"; \
	fi
	@if [ $$(grep -c "FAIL:" api-test-output.log) -gt 0 ]; then \
		exit 1; \
	fi

node-test:
	@echo "Running inference-chain tests..."
	@cd inference-chain && go test ./... -v > ../node-test-output.log
	@echo "----------------------------------------"
	@echo "INFERENCE-CHAIN TEST SUMMARY:"
	@PASS_COUNT=$$(grep -c "PASS:" node-test-output.log); \
	FAIL_COUNT=$$(grep -c "FAIL:" node-test-output.log); \
	NO_TEST_COUNT=$$(grep -c "no test files" node-test-output.log); \
	echo "Passed: $$PASS_COUNT tests"; \
	echo "Failed: $$FAIL_COUNT tests"; \
	echo "No test files: $$NO_TEST_COUNT packages";
	@echo "----------------------------------------"
	@if [ $$(grep -c "FAIL:" node-test-output.log) -gt 0 ]; then \
		echo "Failed tests:"; \
		grep -A 1 "FAIL:" node-test-output.log | grep -v "^\--"; \
	fi
	@if [ $$(grep -c "FAIL:" node-test-output.log) -gt 0 ]; then \
		exit 1; \
	fi

local-build: api-local-build node-local-build api-test node-test
	@echo "=========================================="
	@echo "LOCAL BUILD AND TEST SUMMARY:"
	@API_PASS=$$(grep -c "PASS:" api-test-output.log); \
	API_FAIL=$$(grep -c "FAIL:" api-test-output.log); \
	NODE_PASS=$$(grep -c "PASS:" node-test-output.log); \
	NODE_FAIL=$$(grep -c "FAIL:" node-test-output.log); \
	TOTAL_PASS=$$((API_PASS + NODE_PASS)); \
	TOTAL_FAIL=$$((API_FAIL + NODE_FAIL)); \
	echo "API Tests - Passed: $$API_PASS, Failed: $$API_FAIL"; \
	echo "Node Tests - Passed: $$NODE_PASS, Failed: $$NODE_FAIL"; \
	echo "Total - Passed: $$TOTAL_PASS, Failed: $$TOTAL_FAIL";
	@echo "=========================================="
	@echo "Local build and tests completed successfully!"
	@rm -f api-test-output.log node-test-output.log

build-for-upgrade:
	@rm -f public-html/v2/checksums.txt public-html/v2/urls.txt
	@mkdir -p public-html/v2/inferenced public-html/v2/dapi
	@rm -f public-html/v2/inferenced/inferenced-*.zip public-html/v2/dapi/decentralized-api-*.zip
	@make -C inference-chain build-for-upgrade PLATFORM=linux/amd64 GOOS=linux GOARCH=amd64
	@make -C decentralized-api build-for-upgrade PLATFORM=linux/amd64 GOOS=linux GOARCH=amd64

build-for-upgrade-tests:
	@rm -f public-html/v2/checksums.txt public-html/v2/urls.txt
	@mkdir -p public-html/v2/inferenced public-html/v2/dapi
	@rm -f public-html/v2/inferenced/inferenced-*.zip public-html/v2/dapi/decentralized-api-*.zip
	@make -C inference-chain build-for-upgrade TESTS=1 PLATFORM=linux/amd64 GOOS=linux GOARCH=amd64
	@make -C decentralized-api build-for-upgrade TESTS=1 PLATFORM=linux/amd64 GOOS=linux GOARCH=amd64
