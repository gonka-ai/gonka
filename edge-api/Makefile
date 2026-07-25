.PHONY: all build-docker docker-push release build-for-upgrade build test tidy generate generate-api check-gen validate

include ../scripts/blst-portable.mk

VERSION ?= $(shell git describe --always)
BLST_PORTABLE ?= 0
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
PLATFORM ?= linux/amd64
GOOS ?= linux
GOARCH ?= amd64

BUILD_FLAGS := -ldflags "-X main.version=$(VERSION)"

ifeq ($(BLST_PORTABLE),1)
export CGO_CFLAGS := $(CGO_CFLAGS) -D__BLST_PORTABLE__
endif

USE_REGISTRY_CACHE ?= 0
GHCR_CACHE_NAMESPACE ?= gonka-ai
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/edge-api:buildcache --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/edge-api:buildcache,mode=min
DOCKER_CACHE_ARGS_UPGRADE := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/edge-api:buildcache-upgrade --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/edge-api:buildcache-upgrade,mode=min
DOCKER_BUILD_PREFIX := docker buildx build --load $(DOCKER_CACHE_ARGS)
DOCKER_BUILD_UPGRADE_PREFIX := docker buildx build $(DOCKER_CACHE_ARGS_UPGRADE)
else
DOCKER_CACHE_ARGS :=
DOCKER_CACHE_ARGS_UPGRADE :=
DOCKER_BUILD_PREFIX := DOCKER_BUILDKIT=1 docker build
DOCKER_BUILD_UPGRADE_PREFIX := DOCKER_BUILDKIT=1 docker build
endif

define DOCKER_BUILD
	@echo "--> building edge-api docker image"
	@echo "    	platform: $(PLATFORM)"
	@echo "    	GOOS: $(GOOS)"
	@echo "    	GOARCH: $(GOARCH)"
	@echo "    	BLST_PORTABLE: $(BLST_PORTABLE)"
	@$(DOCKER_BUILD_PREFIX) \
		--platform $(PLATFORM) \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		--build-arg BLST_PORTABLE=$(BLST_PORTABLE) \
		-f Dockerfile \
		.. \
		-t $(DOCKER_TAG)
endef

define DOCKER_BUILD_UPGRADE
	@echo "--> building edge-api upgrade binary"
	@echo "    	platform: $(PLATFORM)"
	@echo "    	GOOS: $(GOOS)"
	@echo "    	GOARCH: $(GOARCH)"
	@$(DOCKER_BUILD_UPGRADE_PREFIX) \
		--platform $(PLATFORM) \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		--build-arg BLST_PORTABLE=$(BLST_PORTABLE) \
		--target binary-exporter \
		-f Dockerfile \
		--output type=local,dest=./output \
		..
endef

all: build-docker

build-docker:
	$(eval PLATFORM=$(DOCKER_PLATFORM))
	$(eval GOOS=$(DOCKER_GOOS))
	$(eval GOARCH=$(DOCKER_GOARCH))
	$(eval DOCKER_TAG=ghcr.io/product-science/edge-api:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/edge-api:latest; \
		docker tag $(DOCKER_TAG) edge-api:latest; \
	fi

docker-push:
	@echo "--> pushing edge-api docker image"
	@echo "pushing to GitHub Container Registry"
	@docker push ghcr.io/product-science/edge-api:$(VERSION)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag ghcr.io/product-science/edge-api:$(VERSION) ghcr.io/product-science/edge-api:latest; \
		docker push ghcr.io/product-science/edge-api:latest; \
	fi

release: build-docker

build-for-upgrade:
	$(DOCKER_BUILD_UPGRADE)
	@echo "--> staging edge-api binary and dependencies for $(GOARCH)"
	@mkdir -p ../public-html/v2/edge-api
	@rm -rf ./output/upgrade-package-$(GOARCH)
	@mkdir -p ./output/upgrade-package-$(GOARCH)
	@cp ./output/build_output/* ./output/upgrade-package-$(GOARCH)/
	@echo "--> zipping edge-api binary with dependencies for $(GOARCH)"
	@TZ=UTC find ./output/upgrade-package-$(GOARCH) -type f -exec touch -t 200001010000 {} \;
	@archive_path="$$(pwd)/../public-html/v2/edge-api/edge-api-$(GOARCH).zip"; \
		rm -f "$$archive_path"; \
		cd ./output/upgrade-package-$(GOARCH) && zip -X -r "$$archive_path" .
	@echo "--> generating shasum for edge-api-$(GOARCH).zip"
	@shasum -a 256 ../public-html/v2/edge-api/edge-api-$(GOARCH).zip
	@echo "--> appending to ../public-html/v2/checksums.txt"
	@checksum=$$(shasum -a 256 ../public-html/v2/edge-api/edge-api-$(GOARCH).zip | awk '{print $$1}'); \
		echo "edge-api-$(GOARCH).zip $$checksum" >> ../public-html/v2/checksums.txt
	@echo "--> cleaning up intermediate build output"
	@rm -rf ./output

generate: generate-api

generate-api:
	$(MAKE) -C ../common generate-queryapi

check-gen: generate-api
	git diff --exit-code ../common/queryapi/gen/

build:
	CGO_ENABLED=1 go build $(BUILD_FLAGS) -o build/edge-api ./cmd/edge-api

test:
	go test ./... -count=1

validate:
	@bash ../scripts/validate-edge-api.sh

tidy:
	go mod tidy
