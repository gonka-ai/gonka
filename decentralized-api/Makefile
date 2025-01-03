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

define DOCKER_RELEASE_BUILD
    @echo "--> building decentralized-api join docker image. platform: $(PLATFORM)"
    @docker build --platform $(PLATFORM) -f DockerfileJoin . -t dapi-join
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
	$(eval PLATFORM=linux/amd64)
	$(DOCKER_BUILD)

release-docker: build-docker
	$(DOCKER_RELEASE_BUILD)

build-docker-amd:
	$(eval PLATFORM=linux/amd64)
	$(DOCKER_BUILD)

docker-push:
	@echo "--> pushing decentralized-api docker image"
	@docker tag decentralized-api gcr.io/decentralized-ai/api
	@docker push gcr.io/decentralized-ai/api

docker-push-release:
	@echo "--> pushing decentralized-api docker image"
	@docker tag dapi-join gcr.io/decentralized-ai/api-join
	@docker push gcr.io/decentralized-ai/api-join

build-and-push-docker: build-docker-amd docker-push

build-v2-test:
	$(eval PLATFORM=linux/amd64)
	$(DOCKER_BUILD)
	@echo "--> clearing out ../public-html/v2/dapi"
	@rm -rf ../public-html/v2/dapi/*
	@mkdir -p ../public-html/v2/dapi
	@echo "--> copying built decentralized-api binary to ../public-html/v2/inferenced"
	@cp ./build/dapi ../public-html/v2/dapi/decentralized-api
	@echo "--> zipping decentralized-api binary"
	@zip -j ../public-html/v2/dapi/decentralized-api.zip ../public-html/v2/dapi/decentralized-api
	@echo "--> generating shasum for decentralized-api.zip"
	@shasum -a 256 ../public-html/v2/dapi/decentralized-api.zip
