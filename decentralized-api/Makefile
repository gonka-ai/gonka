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
ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=decentralized-api \
	-X github.com/cosmos/cosmos-sdk/version.AppName=decentralized-api \
	-X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
	-X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

BUILD_FLAGS := -ldflags '$(ldflags)'

define DOCKER_BUILD
    @echo "--> deleting old ./build/dapi"
	@rm -f ./build/dapi
	@echo "--> building dapi binary locally, outside of docker"
	@env GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o ./build/dapi
	@echo "--> building decentralized-api docker image. platform: $(PLATFORM)"
	@docker build --platform $(PLATFORM) . -t decentralized-api
endef

all:
	install

install:
	@echo "--> ensure dependencies have not been modified"
	@go mod verify
	@echo "--> installing decentralized-api"
	@echo "BUILD_FLAGS = $(BUILD_FLAGS)"
	@go install $(BUILD_FLAGS) -mod=readonly

build:
	@echo "--> ensure dependencies have not been modified"
	@go mod verify
	@echo "--> building decentralized-api"
	@echo "BUILD_FLAGS = $(BUILD_FLAGS)"
	@go build $(BUILD_FLAGS) -o ./build/dapi

build-docker:
	$(eval PLATFORM=linux/arm64)
	$(DOCKER_BUILD)

build-docker-amd:
	$(eval PLATFORM=linux/amd64)
	$(DOCKER_BUILD)

docker-push:
	@echo "--> pushing decentralized-api docker image"
	@docker tag decentralized-api gcr.io/decentralized-ai/api
	@docker push gcr.io/decentralized-ai/api

build-and-push-docker: build-docker-amd docker-push
