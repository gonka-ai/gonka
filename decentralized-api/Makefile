.PHONY: all install build build-docker init clean-state mock-expected-keepers docker-push release build-all package deploy

BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git log -1 --format='%H')

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)

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

define DOCKER_BUILD_UPGRADE
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
	$(eval DOCKER_TAG=gcr.io/decentralized-ai/api:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag gcr.io/decentralized-ai/api:$(VERSION) gcr.io/decentralized-ai/api:latest; \
	fi

docker-push:
	@echo "--> pushing decentralized-api docker image"
	@echo "pushing to Google Cloud Artifact Registry"
	@docker push gcr.io/decentralized-ai/api:$(VERSION)
	@docker tag gcr.io/decentralized-ai/api:$(VERSION) ghcr.io/product-science/api:$(VERSION)
	@echo "pushing to GitHub Container Registry"
	@docker push ghcr.io/product-science/api:$(VERSION)

	@if [ "$(SET_LATEST)" = "1" ]; then \
		@echo "Setting latest tag..."; \
		@docker tag gcr.io/decentralized-ai/api:$(VERSION) gcr.io/decentralized-ai/api:latest; \
		@docker push gcr.io/decentralized-ai/api:latest; \
		@docker tag ghcr.io/product-science/api:$(VERSION) ghcr.io/product-science/api:latest; \
		@docker push ghcr.io/product-science/api:latest; \
	fi

build-for-upgrade:
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_UPGRADE)
	@echo "--> clearing out ../public-html/v2/dapi"
	@rm -rf ../public-html/v2/dapi/*
	@mkdir -p ../public-html/v2/dapi
	@echo "--> copying built decentralized-api binary to ../public-html/v2/inferenced"
	@cp ./output/usr/bin/decentralized-api ../public-html/v2/dapi/decentralized-api-amd64
	@echo "--> zipping decentralized-api binary"
	# We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
	@TZ=UTC touch -t 200001010000 ../public-html/v2/dapi/decentralized-api-amd64
	@zip -X -j ../public-html/v2/dapi/decentralized-api-amd64.zip ../public-html/v2/dapi/decentralized-api-amd64
	@echo "--> generating shasum for decentralized-api.zip"
	@shasum -a 256 ../public-html/v2/dapi/decentralized-api-amd64.zip

build-for-upgrade-arm:
	$(eval PLATFORM=linux/arm64)
	$(eval GOOS=linux)
	$(eval GOARCH=arm64)
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_UPGRADE)
	@mkdir -p ../public-html/v2/dapi
	@echo "--> copying built decentralized-api binary to ../public-html/v2/inferenced"
	@cp ./output/usr/bin/decentralized-api ../public-html/v2/dapi/decentralized-api-arm64
	@echo "--> zipping decentralized-api binary"
	# We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
	@TZ=UTC touch -t 200001010000 ../public-html/v2/dapi/decentralized-api-arm64
	@zip -X -j ../public-html/v2/dapi/decentralized-api-arm64.zip ../public-html/v2/dapi/decentralized-api-arm64
	@echo "--> generating shasum for decentralized-api.zip"
	@shasum -a 256 ../public-html/v2/dapi/decentralized-api-arm64.zip

#################################################
# CROSS-COMPILATION SECTION
#################################################

PLATFORMS := linux darwin windows
ARCHS := amd64 arm64

release: build-docker

build-all:
	@echo "Building all platforms/architectures..."
	@$(foreach os,$(PLATFORMS),\
		$(foreach arch,$(ARCHS),\
			$(MAKE) --no-print-directory build-$(os)-$(arch);))
	@echo "All builds complete."

build-%:
	$(eval OS := $(word 1,$(subst -, ,$*)))
	$(eval ARCH := $(word 2,$(subst -, ,$*)))

	@echo "Building for OS: $(OS), ARCH: $(ARCH)"
	@mkdir -p build/$(OS)/$(ARCH)
	@GOOS=$(OS) GOARCH=$(ARCH) go build -o build/$(OS)/$(ARCH)/decentralized-api

package:
	@echo "Packaging binaries..."
	@mkdir -p release
	@for os in $(PLATFORMS); do \
		for arch in $(ARCHS); do \
			if [ -f build/$$os/$$arch/decentralized-api ]; then \
				zip -j release/decentralized-api-$$os-$$arch.zip \
					build/$$os/$$arch/decentralized-api; \
			fi; \
		done; \
	done