.PHONY: all build-docker release

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
PLATFORMS ?= linux/amd64,linux/arm64
USE_REGISTRY_CACHE ?= 0
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/gonka-ai/proxy-ssl:buildcache --cache-to type=registry,ref=ghcr.io/gonka-ai/proxy-ssl:buildcache,mode=min
else
DOCKER_CACHE_ARGS :=
endif

define DOCKER_BUILD
	@echo "--> building proxy-ssl docker image"
	@echo "    	platform: $(PLATFORM)"
	@docker buildx build \
		--load \
		$(DOCKER_CACHE_ARGS) \
		--platform $(PLATFORM) \
		-f $(DOCKER_FILE) \
		. \
		-t $(DOCKER_TAG)
endef

all: build-docker

build-docker:
	$(eval PLATFORM=linux/amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=ghcr.io/product-science/proxy-ssl:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/proxy-ssl:latest; \
	fi

release:
	@echo "--> releasing proxy-ssl (multi-arch)"
	@echo "platforms: $(PLATFORMS)"
	$(eval DOCKER_FILE=Dockerfile)
	@docker buildx build \
		--platform $(PLATFORMS) \
		-f $(DOCKER_FILE) \
		-t ghcr.io/product-science/proxy-ssl:$(VERSION) \
		--push \
		.
