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

install-arm64-toolchain:
	@echo "--> checking/installing ARM64 cross-compilation toolchain"
	@ARM64_CC=""; \
	if command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then \
		ARM64_CC="aarch64-linux-gnu-gcc"; \
	elif command -v aarch64-linux-musl-gcc >/dev/null 2>&1; then \
		ARM64_CC="aarch64-linux-musl-gcc"; \
	elif ls /usr/local/bin/aarch64-*gcc >/dev/null 2>&1; then \
		ARM64_CC=$$(ls /usr/local/bin/aarch64-*gcc | head -1); \
	fi; \
	if [ -z "$$ARM64_CC" ]; then \
		echo "ARM64 toolchain not found, installing..."; \
		if [[ "$(shell uname)" == "Darwin" ]]; then \
			echo "Installing on macOS..."; \
			if command -v brew >/dev/null 2>&1; then \
				brew install FiloSottile/musl-cross/musl-cross --with-aarch64 || \
				brew install FiloSottile/musl-cross/musl-cross; \
			else \
				echo "Homebrew not found. Please install Homebrew first: https://brew.sh"; \
				exit 1; \
			fi; \
		elif [[ "$(shell uname)" == "Linux" ]]; then \
			echo "Installing on Linux..."; \
			if command -v apt-get >/dev/null 2>&1; then \
				sudo apt-get update && sudo apt-get install -y gcc-aarch64-linux-gnu; \
			elif command -v yum >/dev/null 2>&1; then \
				sudo yum install -y gcc-aarch64-linux-gnu; \
			elif command -v dnf >/dev/null 2>&1; then \
				sudo dnf install -y gcc-aarch64-linux-gnu; \
			else \
				echo "No supported package manager found. Please install gcc-aarch64-linux-gnu manually."; \
				exit 1; \
			fi; \
		else \
			echo "Unsupported OS. Please install aarch64-linux-gnu-gcc manually."; \
			exit 1; \
		fi; \
		if command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then \
			ARM64_CC="aarch64-linux-gnu-gcc"; \
		elif command -v aarch64-linux-musl-gcc >/dev/null 2>&1; then \
			ARM64_CC="aarch64-linux-musl-gcc"; \
		elif ls /usr/local/bin/aarch64-*gcc >/dev/null 2>&1; then \
			ARM64_CC=$$(ls /usr/local/bin/aarch64-*gcc | head -1); \
		else \
			echo "ERROR: ARM64 cross-compiler still not found after installation!"; \
			echo "Please check your installation and ensure the cross-compiler is in PATH"; \
			exit 1; \
		fi; \
	fi; \
	echo "ARM64 cross-compilation toolchain ready: $$ARM64_CC"; \
	mkdir -p build; \
	echo "$$ARM64_CC" > build/arm64_compiler.txt

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
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_UPGRADE)
	@echo "--> clearing out ../public-html/v2/dapi"
	@rm -rf ../public-html/v2/dapi/*
	@mkdir -p ../public-html/v2/dapi
	@echo "--> copying built decentralized-api binary to ../public-html/v2/inferenced"
	@cp ./output/decentralized-api ../public-html/v2/dapi/decentralized-api-amd64
	@echo "--> zipping decentralized-api binary"
	# We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
	@TZ=UTC touch -t 200001010000 ../public-html/v2/dapi/decentralized-api-amd64
	@zip -X -j ../public-html/v2/dapi/decentralized-api-amd64.zip ../public-html/v2/dapi/decentralized-api-amd64
	@echo "--> generating shasum for decentralized-api-amd64.zip"
	@shasum -a 256 ../public-html/v2/dapi/decentralized-api-amd64.zip
	@echo "--> appending to ../public-html/v2/checksums.txt"
	@echo "decentralized-api-amd64.zip $(shasum -a 256 ../public-html/v2/dapi/decentralized-api-amd64.zip)" >> ../public-html/v2/checksums.txt


build-for-upgrade-arm:
	@echo "--> ensuring ARM64 toolchain is available"
	@if [ ! -f build/arm64_compiler.txt ]; then \
		echo "ARM64 toolchain not configured, setting up..."; \
		$(MAKE) install-arm64-toolchain; \
	fi
	@ARM64_CC=$$(cat build/arm64_compiler.txt); \
	echo "Using ARM64 compiler: $$ARM64_CC"; \
	mkdir -p build/lib; \
	echo "--> downloading WASM library for ARM64"; \
	curl -L -o build/lib/libwasmvm_muslc.aarch64.a https://github.com/CosmWasm/wasmvm/releases/download/v1.5.2/libwasmvm_muslc.aarch64.a; \
	echo "--> verifying checksum"; \
	echo "e78b224c15964817a3b75a40e59882b4d0e06fd055b39514d61646689cef8c6e  build/lib/libwasmvm_muslc.aarch64.a" > build/lib/checksum.txt; \
	shasum -a 256 -c build/lib/checksum.txt; \
	ln -sf $(PWD)/build/lib/libwasmvm_muslc.aarch64.a build/lib/libwasmvm_muslc.a; \
	echo "--> building decentralized-api for ARM64 using $$ARM64_CC"; \
	CGO_ENABLED=1 \
	 GOOS=linux \
	 GOARCH=arm64 \
	 CC=$$ARM64_CC \
	 CGO_CFLAGS="-I$(PWD)/build/lib" \
	 CGO_LDFLAGS="-L$(PWD)/build/lib" \
	 go build -mod=readonly -tags muslc $(BUILD_FLAGS) -o ./build/dapi-arm64
	@mkdir -p ../public-html/v2/dapi
	@echo "--> copying built decentralized-api binary to ../public-html/v2/dapi"
	@cp ./build/dapi-arm64 ../public-html/v2/dapi/decentralized-api-arm64
	@echo "--> zipping decentralized-api binary"
	# We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
	@TZ=UTC touch -t 200001010000 ../public-html/v2/dapi/decentralized-api-arm64
	@zip -X -j ../public-html/v2/dapi/decentralized-api-arm64.zip ../public-html/v2/dapi/decentralized-api-arm64
	@echo "--> generating shasum for decentralized-api-arm64.zip"
	@shasum -a 256 ../public-html/v2/dapi/decentralized-api-arm64.zip
	@echo "--> appending to ../public-html/v2/checksums.txt"
	@echo "decentralized-api-arm64.zip $(shasum -a 256 ../public-html/v2/dapi/decentralized-api-arm64.zip)" >> ../public-html/v2/checksums.txt

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