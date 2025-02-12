.PHONY: all install build build-docker init clean-state mock-expected-keepers docker-push release build-all package deploy

BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git log -1 --format='%H')

ifeq (,$(VERSION))
  VERSION := $(shell git describe --exact-match 2>/dev/null)
  ifeq (,$(VERSION))
    VERSION := $(BRANCH)-$(COMMIT)
  endif
endif

ldflags = \
  -X github.com/cosmos/cosmos-sdk/version.Name=inference-chain \
  -X github.com/cosmos/cosmos-sdk/version.AppName=inference-chaind \
  -X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
  -X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

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

define DOCKER_BUILD_OUTPUT
	@echo "--> building inferenced docker image"
	@echo "platform:  $(PLATFORM)"
	@echo "GOARCH:    $(GOARCH)"
	@echo "LDFLAGS:   $(ldflags)"
	@echo "GOOS:      $(GOOS)"

	@DOCKER_BUILDKIT=1 \
	docker build \
		--platform $(PLATFORM) \
		--build-arg LDFLAGS='$(ldflags)' \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
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
	$(eval DOCKER_TAG=gcr.io/decentralized-ai/inferenced)
	$(eval GENESIS_OVERRIDES_FILE=$(GENESIS_OVERRIDES_FILE))
	$(DOCKER_BUILD)

clean-state:
	rm -rf "$(HOME)/.inference"

docker-push:
	docker push gcr.io/decentralized-ai/inferenced

build-for-upgrade:
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(DOCKER_BUILD_OUTPUT)
	@echo "--> clearing out ../public-html/v2/inferenced"
	@rm -rf ../public-html/v2/inferenced/*
	@mkdir -p ../public-html/v2/inferenced
	@echo "--> copying built inferenced binary to ../public-html/v2/inferenced/inferenced"
	@cp ./output/usr/bin/inferenced ../public-html/v2/inferenced/inferenced
	@echo "--> zipping inferenced binary"
	@zip -j ../public-html/v2/inferenced/inferenced.zip ../public-html/v2/inferenced/inferenced
	@echo "--> generating shasum for inferenced.zip"
	@shasum -a 256 ../public-html/v2/inferenced/inferenced.zip

#################################################
# CROSS-COMPILATION SECTION
#################################################

PLATFORMS := linux darwin windows
ARCHS := amd64 arm64

release: build-all package

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
