.PHONY: proto test integration-test stress-test fault-test e2e e2e-ci e2e-images e2e-mock-chain-image e2e-host-image e2e-devshardctl-image

DEVSHARD_E2E_MOCK_CHAIN_IMAGE ?= devshard-mock-chain:local
DEVSHARD_E2E_HOST_IMAGE ?= devshard-host:local
DEVSHARD_E2E_DEVSHARDCTL_IMAGE ?= devshardctl:local
DEVSHARD_E2E_POSTGRES_IMAGE ?= postgres:18.1-bookworm
DOCKER_BUILD ?= DOCKER_BUILDKIT=1 docker build
DEVSHARD_E2E_GO_IMAGE ?= golang:1.24.2-alpine3.20
DEVSHARD_E2E_GO_TMPFS_SIZE ?= 1g

proto:
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=devshard proto/devshard/v1/*.proto

test:
	go test ./... -v

e2e-mock-chain-image:
	$(DOCKER_BUILD) -t $(DEVSHARD_E2E_MOCK_CHAIN_IMAGE) -f Dockerfile.mock-chain .

e2e-host-image:
	$(DOCKER_BUILD) -t $(DEVSHARD_E2E_HOST_IMAGE) -f Dockerfile.host .

e2e-devshardctl-image:
	$(DOCKER_BUILD) -t $(DEVSHARD_E2E_DEVSHARDCTL_IMAGE) -f Dockerfile .

e2e-images: e2e-mock-chain-image e2e-host-image e2e-devshardctl-image
	@echo "Built devshard E2E images:"
	@echo "  DEVSHARD_E2E_MOCK_CHAIN_IMAGE=$(DEVSHARD_E2E_MOCK_CHAIN_IMAGE)"
	@echo "  DEVSHARD_E2E_HOST_IMAGE=$(DEVSHARD_E2E_HOST_IMAGE)"
	@echo "  DEVSHARD_E2E_DEVSHARDCTL_IMAGE=$(DEVSHARD_E2E_DEVSHARDCTL_IMAGE)"

e2e:
	docker run --rm \
		-v "$(CURDIR):/app" \
		-v /var/run/docker.sock:/var/run/docker.sock \
		--tmpfs /tmp:rw,exec,size=$(DEVSHARD_E2E_GO_TMPFS_SIZE) \
		-w /app \
		-e DEVSHARD_E2E=1 \
		-e DEVSHARD_E2E_MOCK_CHAIN_IMAGE=$(DEVSHARD_E2E_MOCK_CHAIN_IMAGE) \
		-e DEVSHARD_E2E_HOST_IMAGE=$(DEVSHARD_E2E_HOST_IMAGE) \
		-e DEVSHARD_E2E_DEVSHARDCTL_IMAGE=$(DEVSHARD_E2E_DEVSHARDCTL_IMAGE) \
		-e DEVSHARD_E2E_POSTGRES_IMAGE=$(DEVSHARD_E2E_POSTGRES_IMAGE) \
		-e GOTMPDIR=/tmp \
		-e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
		$(DEVSHARD_E2E_GO_IMAGE) \
		go test ./e2e -count=1 -v

e2e-ci: e2e-images e2e

integration-test:
	go test ./user/ -tags stress -run 'TestStress|TestFault' -v -timeout 30m -count=1

stress-test:
	go test ./user/ ./storage/ -tags stress -run TestStress -v -timeout 30m -count=1

fault-test:
	go test ./user/ -tags stress -run TestFault -v -timeout 10m -count=1
