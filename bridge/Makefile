.PHONY: all build-docker docker-push release

include ../scripts/blst-portable.mk

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
USE_REGISTRY_CACHE ?= 0
GHCR_CACHE_NAMESPACE ?= gonka-ai
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/bridge:buildcache --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/bridge:buildcache,mode=min
DOCKER_BUILD_PREFIX := docker buildx build --load $(DOCKER_CACHE_ARGS)
else
DOCKER_CACHE_ARGS :=
DOCKER_BUILD_PREFIX := DOCKER_BUILDKIT=1 docker build
endif

define DOCKER_BUILD
	@echo "--> building bridge docker image"
	@echo "    platform: $(PLATFORM)"
	@$(DOCKER_BUILD_PREFIX) \
		--platform $(PLATFORM) \
		-f $(DOCKER_FILE) \
		. \
		-t $(DOCKER_TAG)
endef

all: build-docker

build-docker:
	$(eval PLATFORM=$(DOCKER_PLATFORM))
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=ghcr.io/product-science/bridge:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/bridge:latest; \
	fi

docker-push:
	@echo "--> pushing bridge docker image"
	@echo "pushing to GitHub Container Registry"
	@docker push ghcr.io/product-science/bridge:$(VERSION)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag ghcr.io/product-science/bridge:$(VERSION) ghcr.io/product-science/bridge:latest; \
		docker push ghcr.io/product-science/bridge:latest; \
	fi

release: build-docker

