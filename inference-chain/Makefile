BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git log -1 --format='%H')

# don't override user values
ifeq (,$(VERSION))
  VERSION := $(shell git describe --exact-match 2>/dev/null)
  # if VERSION is empty, then populate it with branch's name and raw commit hash
  ifeq (,$(VERSION))
    VERSION := $(BRANCH)-$(COMMIT)
  endif
endif

# Update the ldflags with the app, client & server names
ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=inference-chain \
	-X github.com/cosmos/cosmos-sdk/version.AppName=inference-chaind \
	-X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
	-X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

BUILD_FLAGS ?= -ldflags '$(ldflags)'

define DOCKER_BUILD
	@echo "--> building inferenced docker image"
	@echo "    	platform: $(PLATFORM)"
	@echo "    	GOARCH: $(GOARCH)"
	@echo "    	BUILD_FLAGS: $(BUILD_FLAGS)"
	@echo "    	GOOS: $(GOOS)"
	@docker build \
		--platform $(PLATFORM) \
		--build-arg BUILD_FLAGS="$(BUILD_FLAGS)" \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		-f $(DOCKER_FILE) \
		.. \
		-t $(DOCKER_TAG)
endef

all:
	install

install:
	@echo "--> ensure dependencies have not been modified"
	@go mod verify
	@echo "--> installing inferenced"
	@echo "BUILD_FLAGS = $(BUILD_FLAGS)"
	@go install $(BUILD_FLAGS) -mod=readonly ./cmd/inferenced/main.go

build:
	@echo "--> ensure dependencies have not been modified"
	@go mod verify
	@go mod tidy
	@echo "--> building inferenced"
	@echo "BUILD_FLAGS = $(BUILD_FLAGS)"
	@CGO_ENABLED=1 CC=musl-gcc \
		go build $(BUILD_FLAGS) -o ./build/inferenced ./cmd/inferenced/main.go

build-docker:
	rm -rf build/
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=inferenced-join)
	$(DOCKER_BUILD)

init:
	./scripts/init.sh

clean-state:
	rm -rf "$$HOME/.inference"

mock-expected-keepers:
	@mockgen -source=x/inference/types/expected_keepers.go \
		-package keeper \
		-destination=testutil/keeper/expected_keepers_mocks.go

docker-push:
	@echo "--> pushing inferenced docker image"
	@docker tag inferenced gcr.io/decentralized-ai/inferenced
	@docker push gcr.io/decentralized-ai/inferenced

docker-push-join:
	@echo "--> pushing inferenced-genesis docker image"
	@docker tag inferenced-join gcr.io/decentralized-ai/inferenced-join
	@docker push gcr.io/decentralized-ai/inferenced-join
