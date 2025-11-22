.PHONY: release decentralized-api-release inference-chain-release tmkms-release proxy-release proxy-ssl-release bridge-release check-docker build-testermint run-blockchain-tests test-blockchain local-build api-local-build node-local-build api-test node-test mock-server-build-docker proxy-build-docker proxy-ssl-build-docker bridge-build-docker run-bls-tests

VERSION ?= $(shell git describe --always)
TAG_NAME := "release/v$(VERSION)"

all: build-docker

build-docker: api-build-docker node-build-docker mock-server-build-docker proxy-build-docker proxy-ssl-build-docker bridge-build-docker

api-build-docker:
	@make -C decentralized-api build-docker SET_LATEST=1

node-build-docker:
	@make -C inference-chain build-docker SET_LATEST=1 $(if $(GENESIS_OVERRIDES_FILE),GENESIS_OVERRIDES_FILE=$(GENESIS_OVERRIDES_FILE),)

mock-server-build-docker:
	@echo "Building mock-server JAR file..."
	@cd testermint/mock_server && ./gradlew clean && ./gradlew shadowJar
	@echo "Building mock-server docker image..."
	@DOCKER_BUILDKIT=1 docker build --load -t inference-mock-server -f testermint/Dockerfile testermint

proxy-build-docker:
	@make -C proxy build-docker SET_LATEST=1

proxy-ssl-build-docker:
	@make -C proxy-ssl build-docker SET_LATEST=1

bridge-build-docker:
	@make -C bridge build-docker SET_LATEST=1

release: decentralized-api-release inference-chain-release tmkms-release proxy-release proxy-ssl-release bridge-release
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

node-local-build:
	@echo "Building inference-chain locally..."
	@make -C inference-chain build

api-test:
	@echo "Running decentralized-api tests..."
	@cd decentralized-api && go test ./... -v > ../api-test-output.log
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
	@rm public-html/v2/checksums.txt || true
	@rm public-html/v2/urls.txt || true
	@make -C inference-chain build-for-upgrade
	@make -C decentralized-api build-for-upgrade

build-for-upgrade-tests:
	@make -C inference-chain build-for-upgrade TESTS=1
	@make -C decentralized-api build-for-upgrade TESTS=1
