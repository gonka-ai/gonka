.PHONY: all install build build-docker init clean-state mock-expected-keepers docker-push release build-all package deploy

BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git log -1 --format='%H')

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)

ldflags = \
  -X github.com/cosmos/cosmos-sdk/version.Name=inference-chain \
  -X github.com/cosmos/cosmos-sdk/version.AppName=inference-chaind \
  -X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
  -X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

GENESIS_OVERRIDES_FILE ?= inference-chain/prod_genesis_overrides.json
TESTS ?= 0
TESTS := $(shell if [ "$(TESTS)" = "1" ]; then echo 1; else echo 0; fi)

install:
	@echo "Installing locally with ldflags..."
	@go install -ldflags "$(ldflags)" -mod=readonly ./cmd/inferenced/main.go

build:
	@echo "Building locally with ldflags..."
	@CGO_ENABLED=1 CC=gcc \
		go build -ldflags "$(ldflags)" -o ./build/inferenced ./cmd/inferenced/main.go
	@if [ "$(shell uname)" = "Darwin" ]; then \
		echo "Running codesign for macOS..."; \
		codesign --remove-signature ./build/inferenced; \
		codesign --force --sign - ./build/inferenced; \
	fi

define DOCKER_BUILD
	@echo "--> building inferenced docker image"
	@echo "platform:  $(PLATFORM)"
	@echo "GOARCH:    $(GOARCH)"
	@echo "LDFLAGS:   $(ldflags)"
	@echo "GOOS:      $(GOOS)"
	@echo "GENESIS_OVERRIDES_FILE: $(GENESIS_OVERRIDES_FILE)"


	@DOCKER_BUILDKIT=1 \
	docker build \
		--platform $(PLATFORM) \
		--build-arg LDFLAGS='$(ldflags)' \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		--build-arg GENESIS_OVERRIDES_FILE=$(GENESIS_OVERRIDES_FILE) \
		-f $(DOCKER_FILE) \
		.. \
		-t $(DOCKER_TAG)
endef

define DOCKER_BUILD_UPGRADE
	@echo "--> building inferenced docker image"
	@echo "platform:  $(PLATFORM)"
	@echo "GOARCH:    $(GOARCH)"
	@echo "LDFLAGS:   $(ldflags)"
	@echo "GOOS:      $(GOOS)"
	@echo "TAGS:      $(if $(filter 1,$(TESTS)),upgraded,not set)"

	@DOCKER_BUILDKIT=1 \
	docker build \
		--platform $(PLATFORM) \
		--build-arg LDFLAGS='$(ldflags)' \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		$(if $(filter 1,$(TESTS)),--build-arg TAGS='upgraded',) \
		-f $(DOCKER_FILE) \
		--output type=local,dest=./output \
		..
endef

build-docker:
	rm -rf build/
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=ghcr.io/product-science/inferenced:$(VERSION))
	$(eval GENESIS_OVERRIDES_FILE=$(GENESIS_OVERRIDES_FILE))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
    	echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/inferenced:latest; \
	fi


clean-state:
	rm -rf "$(HOME)/.inference"

docker-push:
	@echo "pushing to GitHub Container Registry"
	@docker push ghcr.io/product-science/inferenced:$(VERSION)

	@if [ "$(SET_LATEST)" = "1" ]; then \
		@echo "Setting latest tag..."; \
		@docker tag ghcr.io/product-science/inferenced:$(VERSION) ghcr.io/product-science/inferenced:latest; \
		@echo "Pushing latest tag to GitHub Container Registry"; \
		@docker push ghcr.io/product-science/inferenced:latest; \
	fi

build-for-upgrade:
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_UPGRADE)
	@echo "--> clearing out ../public-html/v2/inferenced"
	@rm -rf ../public-html/v2/inferenced/*
	@mkdir -p ../public-html/v2/inferenced
	@echo "--> copying built inferenced binary to ../public-html/v2/inferenced/inferenced"
	@cp ./output/usr/bin/inferenced ../public-html/v2/inferenced/inferenced-amd64
	@echo "--> zipping inferenced binary"
# We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
	@TZ=UTC touch -t 200001010000 ../public-html/v2/inferenced/inferenced-amd64
	@zip -X -j ../public-html/v2/inferenced/inferenced-amd64.zip ../public-html/v2/inferenced/inferenced-amd64
	@echo "--> generating shasum for inferenced.zip"
	@shasum -a 256 ../public-html/v2/inferenced/inferenced-amd64.zip

build-for-upgrade-arm:
	$(eval PLATFORM=linux/arm64)
	$(eval GOOS=linux)
	$(eval GOARCH=arm64)
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_UPGRADE)
	@mkdir -p ../public-html/v2/inferenced
	@echo "--> copying built inferenced binary to ../public-html/v2/inferenced/inferenced"
	@cp ./output/usr/bin/inferenced ../public-html/v2/inferenced/inferenced-arm64
	@echo "--> zipping inferenced binary"
# We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
	@TZ=UTC touch -t 200001010000 ../public-html/v2/inferenced/inferenced-arm64
	@zip -X -j ../public-html/v2/inferenced/inferenced-arm64.zip ../public-html/v2/inferenced/inferenced-arm64
	@echo "--> generating shasum for inferenced.zip"
	@shasum -a 256 ../public-html/v2/inferenced/inferenced-arm64.zip


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
	@GOOS=$(OS) GOARCH=$(ARCH) go build -ldflags "$(ldflags)" -o build/$(OS)/$(ARCH)/inferenced ./cmd/inferenced/main.go
	@if [ "$(OS)" = "darwin" ]; then \
		echo "Running codesign for macOS (universal binary build may vary)..."; \
		codesign --remove-signature build/$(OS)/$(ARCH)/inferenced || true; \
		codesign --force --sign - build/$(OS)/$(ARCH)/inferenced || true; \
	fi

package:
	@echo "Packaging binaries..."
	@mkdir -p release
	@for os in $(PLATFORMS); do \
		for arch in $(ARCHS); do \
			if [ -f build/$$os/$$arch/inferenced ]; then \
				zip -j release/inferenced-$$os-$$arch.zip build/$$os/$$arch/inferenced; \
			fi; \
		done; \
	done

mock-expected-keepers:
	@mockgen -source=x/inference/types/expected_keepers.go \
		-package keeper \
		-destination=testutil/keeper/expected_keepers_mocks.go
