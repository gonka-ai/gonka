.PHONY: proto test gateway-mockenv-test gateway-mockenv-test-junit integration-test stress-test fault-test ci-testenv-unit ci-testenv-integration devshard-gateway-release e2e e2e-test e2e-images

DEVSHARD_VERSION ?= dev
DEVSHARD_PROTOCOL_VERSION ?= v3
DEVSHARD_GATEWAY_IMAGE ?= ghcr.io/gonka-ai/devshard-gateway
DEVSHARD_GATEWAY_TAGS ?=
PLATFORM ?= linux/amd64
GATEWAY_MOCKENV_TEST_PACKAGE ?= ./cmd/devshardctl
GATEWAY_MOCKENV_TEST_PATTERN ?= GatewayMockEnv
GATEWAY_MOCKENV_JUNIT_REPORT ?= mockenv-junit-report.xml

DEVSHARD_E2E_MOCK_CHAIN_IMAGE ?= devshard-mock-chain:local
DEVSHARD_E2E_HOST_IMAGE ?= devshard-host:local
DEVSHARD_E2E_DEVSHARDCTL_IMAGE ?= devshardctl:local
DEVSHARD_E2E_POSTGRES_IMAGE ?= postgres:18.1-bookworm
# Pin e2e host+ctl protocol stamp to "dev" so images stay aligned without
# bumping this on every release protocol version change.
DEVSHARD_E2E_VERSION ?= $(DEVSHARD_VERSION)
DOCKER_BUILD ?= DOCKER_BUILDKIT=1 docker build
DEVSHARD_E2E_GO_IMAGE ?= golang:1.25.9-alpine3.23
DEVSHARD_E2E_GO_TMPFS_SIZE ?= 1g
DEVSHARD_E2E_GOTESTSUM ?= gotest.tools/gotestsum@v1.13.0

devshard-gateway-release:
	@if [ -z "$(strip $(DEVSHARD_GATEWAY_TAGS))" ]; then \
		echo "DEVSHARD_GATEWAY_TAGS is required" >&2; \
		exit 1; \
	fi
	docker buildx build \
		--platform $(PLATFORM) \
		--target devshardctl-runtime \
		--build-arg DEVSHARD_VERSION=$(DEVSHARD_VERSION) \
		--build-arg DEVSHARD_PROTOCOL_VERSION=$(DEVSHARD_PROTOCOL_VERSION) \
		$(foreach tag,$(strip $(DEVSHARD_GATEWAY_TAGS)),-t $(DEVSHARD_GATEWAY_IMAGE):$(tag)) \
		-f Dockerfile \
		--push \
		..

proto:
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=devshard proto/devshard/v1/*.proto

test:
	go test ./... -v

gateway-mockenv-test:
	go test $(GATEWAY_MOCKENV_TEST_PACKAGE) -run $(GATEWAY_MOCKENV_TEST_PATTERN) -count=1 -v

gateway-mockenv-test-junit:
	go test $(GATEWAY_MOCKENV_TEST_PACKAGE) -run $(GATEWAY_MOCKENV_TEST_PATTERN) -count=1 -v 2>&1 | go-junit-report -set-exit-code > $(GATEWAY_MOCKENV_JUNIT_REPORT)

# Phase 11 — testenv v2 unit smoke (no Docker). Run from repo root paths via sub-makes.
ci-testenv-unit:
	$(MAKE) -C ../common ci-testenv-runtimeconfig
	go test ./chainoracle/... -count=1
	go test ./runtimeparams/... -count=1
	go test ./runtimeconfig/... -count=1
	go test ./testenv/... -count=1
	$(MAKE) -C ../decentralized-api ci-testenv-nodemanager

# Phase 11 — Docker stack citest (dispatch / nightly). Requires Docker + linux devshardd.
ci-testenv-integration:
	$(MAKE) -C testenv build-devshardd
	$(MAKE) -C testenv citest-stack
	$(MAKE) -C testenv citest-grpc-transport
	$(MAKE) -C testenv citest-adversarial

e2e-images:
	$(DOCKER_BUILD) -t $(DEVSHARD_E2E_MOCK_CHAIN_IMAGE) -f Dockerfile.mock-chain ..
	$(DOCKER_BUILD) --build-arg DEVSHARD_VERSION=$(DEVSHARD_E2E_VERSION) -t $(DEVSHARD_E2E_HOST_IMAGE) -f Dockerfile.host ..
	$(DOCKER_BUILD) --build-arg DEVSHARD_VERSION=$(DEVSHARD_E2E_VERSION) --target devshardctl-runtime -t $(DEVSHARD_E2E_DEVSHARDCTL_IMAGE) -f Dockerfile ..

e2e-test:
	docker compose -f docker-compose.e2e.yml run --rm e2e-test

e2e: e2e-images e2e-test

integration-test:
	go test ./user/ -tags stress -run 'TestStress|TestFault' -v -timeout 30m -count=1

stress-test:
	go test ./user/ ./storage/ -tags stress -run TestStress -v -timeout 30m -count=1

fault-test:
	go test ./user/ -tags stress -run TestFault -v -timeout 10m -count=1
