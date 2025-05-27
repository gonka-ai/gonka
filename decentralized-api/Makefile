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

# Component-specific names
BINARY_NAME := decentralized-api
COMPONENT_SUBDIR := dapi

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

# Macro to process upgrade artifacts (copy, zip, checksum, add to urls)
# Arg: ARCH
define PROCESS_UPGRADE_ARTIFACT
    $(eval _P_ARCH := $(1))

    @echo "--- Processing upgrade artifact for decentralized-api-$(_P_ARCH) ---"
    @mkdir -p ../public-html/v2/dapi
    @echo "--> copying built decentralized-api binary to ../public-html/v2/dapi/decentralized-api-$(_P_ARCH)"
    @cp ./output/usr/bin/decentralized-api ../public-html/v2/dapi/decentralized-api-$(_P_ARCH)
    @echo "--> zipping decentralized-api-$(_P_ARCH) binary"
    # We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
    @TZ=UTC touch -t 200001010000 ../public-html/v2/dapi/decentralized-api-$(_P_ARCH)
    @echo "--- Verifying $(_P_ARCH) zip file before shasum (pre-zip step for decentralized-api-$(_P_ARCH)) ---"
    @ls -l ../public-html/v2/dapi/decentralized-api-$(_P_ARCH)
    @zip -X -j ../public-html/v2/dapi/decentralized-api-$(_P_ARCH).zip ../public-html/v2/dapi/decentralized-api-$(_P_ARCH)
    @echo "--- Verifying $(_P_ARCH) zip file existence and size (post-zip for decentralized-api-$(_P_ARCH)) ---"
    @ls -l ../public-html/v2/dapi/decentralized-api-$(_P_ARCH).zip
    @echo "--> generating shasum for decentralized-api-$(_P_ARCH).zip"
    @echo "--- Raw shasum output for $(_P_ARCH) (decentralized-api-$(_P_ARCH).zip) (before cut) ---"
    @shasum -a 256 ../public-html/v2/dapi/decentralized-api-$(_P_ARCH).zip

    $(eval _TMP_SHA256 := $(shell shasum -a 256 ../public-html/v2/dapi/decentralized-api-$(_P_ARCH).zip 2>/dev/null | cut -d' ' -f1))

    @echo "$(_P_ARCH) SHA256 for decentralized-api: $(_TMP_SHA256)"
    @echo "--> appending to ../public-html/v2/checksums.txt"
    @echo "decentralized-api-$(_P_ARCH).zip $(_TMP_SHA256)" >> ../public-html/v2/checksums.txt
    @echo "--> appending to ../public-html/v2/urls.txt"
    @echo "https://github.com/product-science/race-releases/releases/download/release%2F$(VERSION)/decentralized-api-$(_P_ARCH).zip?checksum=sha256:$(_TMP_SHA256)" >> ../public-html/v2/urls.txt
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
	$(call PROCESS_UPGRADE_ARTIFACT,amd64)

build-for-upgrade-arm:
	$(eval PLATFORM=linux/arm64)
	$(eval GOOS=linux)
	$(eval GOARCH=arm64)
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_UPGRADE)
	# Note: No clearing of COMPONENT_SUBDIR for arm, assume build-for-upgrade already cleared it
	$(call PROCESS_UPGRADE_ARTIFACT,arm64)

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