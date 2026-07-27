.PHONY: all install build build-docker init clean-state mock-expected-keepers docker-push release build-all package deploy test generate

BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git log -1 --format='%H')

include ../scripts/blst-portable.mk

VERSION ?= $(shell git describe --always)
DEVSHARD_VERSION ?= $(VERSION)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
PLATFORM ?= linux/amd64
GOOS ?= linux
GOARCH ?= amd64

ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=decentralized-api \
	-X github.com/cosmos/cosmos-sdk/version.AppName=decentralized-api \
	-X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
	-X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

BUILD_FLAGS ?= -ldflags '$(ldflags)'

WASMVM_STATIC_LINUX_X64_URL := https://github.com/CosmWasm/wasmvm/releases/download/v2.2.4/libwasmvm_muslc.x86_64.a

USE_REGISTRY_CACHE ?= 0
GHCR_CACHE_NAMESPACE ?= gonka-ai
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/api:buildcache --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/api:buildcache,mode=min
DOCKER_CACHE_ARGS_UPGRADE := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/api:buildcache-upgrade --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/api:buildcache-upgrade,mode=min
DOCKER_BUILD_PREFIX := docker buildx build --load $(DOCKER_CACHE_ARGS)
DOCKER_BUILD_UPGRADE_PREFIX := docker buildx build $(DOCKER_CACHE_ARGS_UPGRADE)
else
DOCKER_CACHE_ARGS :=
DOCKER_CACHE_ARGS_UPGRADE :=
DOCKER_BUILD_PREFIX := DOCKER_BUILDKIT=1 docker build
DOCKER_BUILD_UPGRADE_PREFIX := DOCKER_BUILDKIT=1 docker build
endif

define DOCKER_BUILD
	@echo "--> building decentralized-api docker image"
	@echo "    	platform: $(PLATFORM)"
	@echo "    	GOOS: $(GOOS)"
	@echo "    	GOARCH: $(GOARCH)"
	@echo "    	BLST_PORTABLE: $(BLST_PORTABLE)"
	@$(DOCKER_BUILD_PREFIX) \
		--platform $(PLATFORM) \
		--build-arg LDFLAGS='$(ldflags)' \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		--build-arg BLST_PORTABLE=$(BLST_PORTABLE) \
		--build-arg DEVSHARD_VERSION=$(DEVSHARD_VERSION) \
		-f $(DOCKER_FILE) \
		.. \
		-t $(DOCKER_TAG)
endef

define DOCKER_BUILD_UPGRADE
	@echo "--> building decentralized-api docker image"
	@echo "    	platform: $(PLATFORM)"
	@echo "    	GOOS: $(GOOS)"
	@echo "    	GOARCH: $(GOARCH)"
	@echo "    	BLST_PORTABLE: $(BLST_PORTABLE)"
	@$(DOCKER_BUILD_UPGRADE_PREFIX) \
		--platform $(PLATFORM) \
		--build-arg LDFLAGS='$(ldflags)' \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		--build-arg DEVSHARD_VERSION=$(DEVSHARD_VERSION) \
		--target binary-exporter \
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

build: download-linux-deps
	@echo "--> ensure dependencies have not been modified"
	@go mod verify
	@echo "--> building decentralized-api"
	@echo "BUILD_FLAGS = $(BUILD_FLAGS)"
	@CGO_ENABLED=1 CC=gcc \
		CGO_LDFLAGS="-L$(PWD)/build/deps -lwasmvm_muslc -lm" \
		go build $(BUILD_FLAGS) -tags muslc -o ./build/dapi

include ../scripts/blst-portable.mk

build-docker:
	rm -rf build/
	$(eval PLATFORM=$(DOCKER_PLATFORM))
	$(eval GOOS=$(DOCKER_GOOS))
	$(eval GOARCH=$(DOCKER_GOARCH))
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=ghcr.io/product-science/api:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/api:latest; \
	fi

docker-push:
	@echo "--> pushing decentralized-api docker image"
	@echo "pushing to GitHub Container Registry"
	@docker push ghcr.io/product-science/api:$(VERSION)

	@if [ "$(SET_LATEST)" = "1" ]; then \
		@echo "Setting latest tag..."; \
		@docker tag ghcr.io/product-science/api:$(VERSION) ghcr.io/product-science/api:latest; \
		@docker push ghcr.io/product-science/api:latest; \
	fi

build-for-upgrade:
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_UPGRADE)
	@echo "--> staging decentralized-api binary and dependencies for $(GOARCH)"
	@mkdir -p ../public-html/v2/dapi
	@rm -rf ./output/upgrade-package-$(GOARCH)
	@mkdir -p ./output/upgrade-package-$(GOARCH)
	@cp ./output/build_output/* ./output/upgrade-package-$(GOARCH)/
	@echo "--> zipping decentralized-api binary with dependencies for $(GOARCH)"
	# We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
	@TZ=UTC find ./output/upgrade-package-$(GOARCH) -type f -exec touch -t 200001010000 {} \;
	@archive_path="$$(pwd)/../public-html/v2/dapi/decentralized-api-$(GOARCH).zip"; \
		rm -f "$$archive_path"; \
		cd ./output/upgrade-package-$(GOARCH) && zip -X -r "$$archive_path" .
	@echo "--> generating shasum for decentralized-api-$(GOARCH).zip"
	@shasum -a 256 ../public-html/v2/dapi/decentralized-api-$(GOARCH).zip
	@echo "--> appending to ../public-html/v2/checksums.txt"
	@checksum=$$(shasum -a 256 ../public-html/v2/dapi/decentralized-api-$(GOARCH).zip | awk '{print $$1}'); \
		echo "decentralized-api-$(GOARCH).zip $$checksum" >> ../public-html/v2/checksums.txt
	@echo "--> cleaning up intermediate build output"
	@rm -rf ./output

download-linux-deps:
	@echo "--> Downloading static wasmvm library for Linux (x86_64)..."
	@mkdir -p build/deps
	@if [ ! -f build/deps/libwasmvm_muslc.x86_64.a ]; then \
		curl -L -o build/deps/libwasmvm_muslc.x86_64.a $(WASMVM_STATIC_LINUX_X64_URL); \
	fi
	@ln -sf libwasmvm_muslc.x86_64.a build/deps/libwasmvm_muslc.a
	@echo "API dependencies downloaded to ./build/deps/"

generate:
	@echo "--> running go generate"
	@go generate ./...

test:
	@echo "--> running tests"
	@go test ./... -timeout 120s
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
