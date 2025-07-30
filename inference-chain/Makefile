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
		--load \
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
		--target binary-exporter \
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
	@echo "--> copying built inferenced binary and dependencies to ../public-html/v2/inferenced/"
	@cp ./output/build_output/* ../public-html/v2/inferenced/
	@echo "--> zipping inferenced binary with dependencies"
	# We set the timestamp to a const and strip metadata in zip so we have a reproduceable checksum
	@TZ=UTC find ../public-html/v2/inferenced -type f -exec touch -t 200001010000 {} \;
	@cd ../public-html/v2/inferenced && zip -X -r inferenced-amd64.zip .
	@echo "--> generating shasum for inferenced-amd64.zip"
	@shasum -a 256 ../public-html/v2/inferenced/inferenced-amd64.zip
	@echo "--> appending to ../public-html/v2/checksums.txt"
	@echo "inferenced-amd64.zip $(shasum -a 256 ../public-html/v2/inferenced/inferenced-amd64.zip)" >> ../public-html/v2/checksums.txt

# Disabled ARM builds as requested
build-for-upgrade-arm:
	@echo "ARM builds disabled"

#################################################
# CROSS-COMPILATION SECTION
#################################################

PLATFORMS := linux darwin windows
ARCHS := amd64 arm64

release: build-docker

build-all:
	@echo "Building all platforms/architectures..."
	@echo "Building native Darwin binaries..."
	@$(MAKE) --no-print-directory build-darwin-amd64 build-darwin-arm64
	@echo "Building Linux binaries via Docker..."
	@$(MAKE) --no-print-directory build-linux-docker
	@echo "Note: Windows builds require additional setup. Use build-windows-docker if needed."
	@echo "All builds complete."

build-windows-docker:
	@echo "Building Windows binary using Docker (amd64 only)..."
	@mkdir -p build/windows/amd64
	@docker run --rm --platform linux/amd64 \
		-v "$(PWD)":/workspace \
		-w /workspace \
		golang:1.23-bullseye \
		bash -c '\
			echo "Installing mingw-w64..." && \
			apt-get update -qq && apt-get install -y -qq gcc-mingw-w64 && \
			echo "Building windows/amd64..." && \
			CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 go build -ldflags "$(ldflags)" -o build/windows/amd64/inferenced.exe ./cmd/inferenced/main.go && \
			echo "Windows build complete!"'

build-linux-docker:
	@echo "Building Linux binaries using Docker..."
	@mkdir -p build/linux/amd64 build/linux/arm64
	@docker run --rm --platform linux/amd64 \
		-v "$(PWD)":/workspace \
		-w /workspace \
		golang:1.23-bullseye \
		bash -c '\
			echo "Installing dependencies..." && \
			apt-get update -qq && apt-get install -y -qq gcc gcc-aarch64-linux-gnu && \
			echo "Downloading Go modules..." && \
			go mod download && \
			echo "Building linux/amd64..." && \
			CGO_ENABLED=1 CC=gcc GOOS=linux GOARCH=amd64 go build -ldflags "$(ldflags)" -o build/linux/amd64/inferenced ./cmd/inferenced/main.go && \
			echo "Building linux/arm64..." && \
			CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc GOOS=linux GOARCH=arm64 go build -ldflags "$(ldflags)" -o build/linux/arm64/inferenced ./cmd/inferenced/main.go && \
			echo "Linux builds complete!"'



build-%:
	$(eval OS := $(word 1,$(subst -, ,$*)))
	$(eval ARCH := $(word 2,$(subst -, ,$*)))

	@echo "Building for OS: $(OS), ARCH: $(ARCH)"
	@mkdir -p build/$(OS)/$(ARCH)
	@if [ "$(OS)" = "$(shell go env GOOS)" ]; then \
		echo "Building for native platform $(OS)/$(ARCH) with CGO..."; \
		CGO_ENABLED=1 GOOS=$(OS) GOARCH=$(ARCH) go build -ldflags "$(ldflags)" -tags "!test" -o build/$(OS)/$(ARCH)/inferenced ./cmd/inferenced/main.go; \
	else \
		echo "Skipping cross-compilation for $(OS)/$(ARCH) (requires platform-specific setup)"; \
		echo "Use 'make build-all-docker' for cross-compilation or build on target platform"; \
		mkdir -p build/$(OS)/$(ARCH) && touch build/$(OS)/$(ARCH)/.skipped; \
	fi
	@if [ "$(OS)" = "darwin" ] && [ -f "build/$(OS)/$(ARCH)/inferenced" ]; then \
		echo "Running codesign for macOS..."; \
		codesign --remove-signature build/$(OS)/$(ARCH)/inferenced || true; \
		codesign --force --sign - build/$(OS)/$(ARCH)/inferenced || true; \
	fi

package:
	@echo "Packaging binaries..."
	@mkdir -p release
	@for os in $(PLATFORMS); do \
		for arch in $(ARCHS); do \
			if [ -f build/$$os/$$arch/inferenced ] && [ ! -f build/$$os/$$arch/.skipped ]; then \
				echo "Packaging $$os/$$arch..."; \
				zip -j release/inferenced-$$os-$$arch.zip build/$$os/$$arch/inferenced; \
			else \
				echo "Skipping $$os/$$arch (no binary found)"; \
			fi; \
		done; \
	done

mock-expected-keepers:
	@mockgen -source=x/inference/types/expected_keepers.go \
		-package keeper \
		-destination=testutil/keeper/expected_keepers_mocks.go
