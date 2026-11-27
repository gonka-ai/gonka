.PHONY: proto test test-junit integration-test stress-test fault-test ci-testenv-unit ci-testenv-integration e2e e2e-test e2e-images snapshot-recovery-stress-test
.PHONY: print-e2e-version

DEVSHARD_VERSION ?= dev
DEVSHARD_GOTESTSUM ?= gotest.tools/gotestsum@v1.13.0
DEVSHARD_TEST_PACKAGES ?= ./...
DEVSHARD_JUNIT_REPORT ?= junit-report.xml
DEVSHARD_TEST_FORMAT ?= testname

DEVSHARD_E2E_MOCK_CHAIN_IMAGE ?= devshard-mock-chain:local
DEVSHARD_E2E_HOST_IMAGE ?= devshard-host:local
DEVSHARD_E2E_GATEWAY_IMAGE ?= devshard-gateway:local
DEVSHARD_E2E_POSTGRES_IMAGE ?= postgres:18.1-bookworm
# Pin e2e host+gateway protocol stamp to "dev" so images stay aligned without
# bumping this on every release protocol version change.
DEVSHARD_E2E_VERSION ?= $(DEVSHARD_VERSION)
DOCKER_BUILD ?= DOCKER_BUILDKIT=1 docker build
DEVSHARD_E2E_GO_IMAGE ?= golang:1.25.9-alpine3.23
DEVSHARD_E2E_GO_TMPFS_SIZE ?= 1g
DEVSHARD_E2E_GOTESTSUM ?= $(DEVSHARD_GOTESTSUM)

proto:
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=devshard proto/devshard/v1/*.proto
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=devshard \
		--connect-go_out=. --connect-go_opt=module=devshard \
		proto/devshard/transport/v1/*.proto

test:
	go run $(DEVSHARD_GOTESTSUM) --format $(DEVSHARD_TEST_FORMAT) -- $(DEVSHARD_TEST_PACKAGES)

test-junit:
	go run $(DEVSHARD_GOTESTSUM) --format $(DEVSHARD_TEST_FORMAT) --junitfile $(DEVSHARD_JUNIT_REPORT) -- $(DEVSHARD_TEST_PACKAGES)

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

# CI image builds use the same resolved version as local e2e-images builds.
print-e2e-version:
	@printf '%s\n' "$(DEVSHARD_E2E_VERSION)"

e2e-images:
	$(DOCKER_BUILD) -t $(DEVSHARD_E2E_MOCK_CHAIN_IMAGE) -f Dockerfile.mock-chain ..
	$(DOCKER_BUILD) --build-arg DEVSHARD_VERSION=$(DEVSHARD_E2E_VERSION) -t $(DEVSHARD_E2E_HOST_IMAGE) -f Dockerfile.host ..
	# The gateway owns how its own image is built; the stack only says which tag it wants.
	$(MAKE) -C cmd/gateway image GATEWAY_VERSION=$(DEVSHARD_E2E_VERSION) DEVSHARD_PROTOCOL_VERSION=$(DEVSHARD_E2E_VERSION) \
		GATEWAY_IMAGE=$(firstword $(subst :, ,$(DEVSHARD_E2E_GATEWAY_IMAGE))) \
		GATEWAY_STAGE_TAG=$(lastword $(subst :, ,$(DEVSHARD_E2E_GATEWAY_IMAGE)))

e2e-test:
	docker compose -f docker-compose.e2e.yml run --rm e2e-test

e2e: e2e-images e2e-test

integration-test:
	go test ./user/ -tags stress -run 'TestStress|TestFault' -v -timeout 30m -count=1

stress-test:
	go test ./user/ ./storage/ -tags stress -run TestStress -v -timeout 30m -count=1

snapshot-recovery-stress-test:
	go test ./user/ -tags stress -run TestStressSnapshotRecoveryBudget -timeout 30m -count=1

fault-test:
	go test ./user/ -tags stress -run TestFault -v -timeout 10m -count=1
