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

BUILD_FLAGS := -ldflags '$(ldflags)'

define DOCKER_BUILD
    @echo "--> deleting old ./build/inferenced"
	@rm -f ./build/inferenced
	@echo "--> building inferenced binary locally, outside of docker"
	@env GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o ./build/inferenced ./cmd/inferenced/main.go
	@echo "--> building inferenced docker image. platform: $(PLATFORM)"
	@docker build --platform $(PLATFORM) . -t inferenced
endef

define DOCKER_GENESIS_BUILD
    @echo "--> deleting old ./build/inferenced"
	@rm -f ./build/inferenced
	@echo "--> building inferenced binary locally, outside of docker"
	@env GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o ./build/inferenced ./cmd/inferenced/main.go
	@echo "--> building inferenced docker image. platform: $(PLATFORM)"
	@docker build -f DockerfileGenesis --platform $(PLATFORM) . -t inferenced-genesis
endef


define DOCKER_RELEASE_BUILD
    @echo "--> deleting old ./build/inferenced"
	@rm -f ./build/inferenced
	@echo "--> building inferenced binary locally, outside of docker"
	@env GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o ./build/inferenced ./cmd/inferenced/main.go
	@echo "--> building inferenced docker image. platform: $(PLATFORM)"
    @docker build -f DockerfileJoin --platform $(PLATFORM) . -t inferenced-join
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
	@go build $(BUILD_FLAGS) -o ./build/inferenced ./cmd/inferenced/main.go

build-docker:
	$(eval PLATFORM=linux/arm64)
	$(DOCKER_BUILD)

build-genesis:
	$(eval PLATFORM=linux/amd64)
	$(DOCKER_GENESIS_BUILD)

release-docker:
	$(eval PLATFORM=linux/amd64)
	$(DOCKER_RELEASE_BUILD)

build-docker-amd:
	$(eval PLATFORM=linux/amd64)
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

docker-push-genesis:
	@echo "--> pushing inferenced-genesis docker image"
	@docker tag inferenced-genesis gcr.io/decentralized-ai/inferenced-genesis
	@docker push gcr.io/decentralized-ai/inferenced-genesis

docker-push-join:
	@echo "--> pushing inferenced-genesis docker image"
	@docker tag inferenced-join gcr.io/decentralized-ai/inferenced-join
	@docker push gcr.io/decentralized-ai/inferenced-join

build-and-push-docker: build-docker-amd docker-push

build-v2-test:
	$(eval PLATFORM=linux/amd64)
	$(DOCKER_BUILD)
	@echo "--> clearing out ../public-html/v2"
	@rm -rf ../public-html/v2/*
	@mkdir -p ../public-html/v2
	@echo "--> copying built inferenced binary to ../public-html/v2/inferenced"
	@cp ./build/inferenced ../public-html/v2/inferenced
	@echo "--> zipping inferenced binary"
	@zip -j ../public-html/v2/inferenced.zip ../public-html/v2/inferenced
	@echo "--> generating shasum for inferenced.zip"
	@shasum -a 256 ../public-html/v2/inferenced.zip
