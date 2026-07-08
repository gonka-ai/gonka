.PHONY: proto test integration-test stress-test fault-test e2e e2e-test e2e-images

DEVSHARD_E2E_MOCK_CHAIN_IMAGE ?= devshard-mock-chain:local
DEVSHARD_E2E_HOST_IMAGE ?= devshard-host:local
DEVSHARD_E2E_DEVSHARDCTL_IMAGE ?= devshardctl:local
DEVSHARD_E2E_POSTGRES_IMAGE ?= postgres:18.1-bookworm
DOCKER_BUILD ?= DOCKER_BUILDKIT=1 docker build
DEVSHARD_E2E_GO_IMAGE ?= golang:1.25.9-alpine3.23
DEVSHARD_E2E_GO_TMPFS_SIZE ?= 1g
DEVSHARD_E2E_GOTESTSUM ?= gotest.tools/gotestsum@v1.12.0

proto:
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=devshard proto/devshard/v1/*.proto

test:
	go test ./... -v

e2e-images:
	$(DOCKER_BUILD) -t $(DEVSHARD_E2E_MOCK_CHAIN_IMAGE) -f Dockerfile.mock-chain .
	$(DOCKER_BUILD) -t $(DEVSHARD_E2E_HOST_IMAGE) -f Dockerfile.host .
	$(DOCKER_BUILD) -t $(DEVSHARD_E2E_DEVSHARDCTL_IMAGE) -f Dockerfile .

e2e-test:
	docker compose -f docker-compose.e2e.yml run --rm e2e-test

e2e: e2e-images e2e-test

integration-test:
	go test ./user/ -tags stress -run 'TestStress|TestFault' -v -timeout 30m -count=1

stress-test:
	go test ./user/ ./storage/ -tags stress -run TestStress -v -timeout 30m -count=1

fault-test:
	go test ./user/ -tags stress -run TestFault -v -timeout 10m -count=1
