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

BUILD_FLAGS ?= -ldflags '$(ldflags)'

define DOCKER_BUILD
	@echo "--> building decentralized-api docker image"
	@echo "    	platform: $(PLATFORM)"
	@echo "    	BUILD_FLAGS: $(BUILD_FLAGS)"
	@echo "    	GOOS: $(GOOS)"
	@echo "    	GOARCH: $(GOARCH)"
	@docker build \
		--platform $(PLATFORM) \
		--build-arg BUILD_FLAGS="$(BUILD_FLAGS)" \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		-f $(DOCKER_FILE) \
		.. \
		-t $(DOCKER_TAG)
endef

define DOCKER_BUILD_OUTPUT
	@echo "--> building decentralized-api docker image"
	@echo "    	platform: $(PLATFORM)"
	@echo "    	BUILD_FLAGS: $(BUILD_FLAGS)"
	@echo "    	GOOS: $(GOOS)"
	@echo "    	GOARCH: $(GOARCH)"
	@docker build \
		--platform $(PLATFORM) \
		--build-arg BUILD_FLAGS="$(BUILD_FLAGS)" \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		-f $(DOCKER_FILE) \
		--output type=local,dest=./output \
		..
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
	@CGO_ENABLED=1 CC=gcc \
		go build $(BUILD_FLAGS) -o ./build/dapi

build-docker:
	rm -rf build/
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=gcr.io/decentralized-ai/api)
	$(DOCKER_BUILD)

docker-push:
	@echo "--> pushing decentralized-api docker image"
	@docker push gcr.io/decentralized-ai/api

build-v2-test:
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_OUTPUT)
	@echo "--> clearing out ../public-html/v2/dapi"
	@rm -rf ../public-html/v2/dapi/*
	@mkdir -p ../public-html/v2/dapi
	@echo "--> copying built decentralized-api binary to ../public-html/v2/inferenced"
	@cp ./output/usr/bin/decentralized-api ../public-html/v2/dapi/decentralized-api
	@echo "--> zipping decentralized-api binary"
	@zip -j ../public-html/v2/dapi/decentralized-api.zip ../public-html/v2/dapi/decentralized-api
	@echo "--> generating shasum for decentralized-api.zip"
	@shasum -a 256 ../public-html/v2/dapi/decentralized-api.zip
