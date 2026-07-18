.PHONY: all build-docker release test-render

test-render:
	@tmpdir=$$(mktemp -d); \
	VERSIOND_HOSTS='versiond versiond2' \
	VERSIOND_LEGACY_HOST=versiond \
	VERSIOND_NON_HA_VERSIONS='v1,v2 v3' \
	VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/mixed.conf" \
	./entrypoint.sh >/dev/null; \
	grep -q 'default versiond_ha_pool' "$$tmpdir/mixed.conf"; \
	grep -q 'v1 versiond_legacy' "$$tmpdir/mixed.conf"; \
	grep -q 'v2 versiond_legacy' "$$tmpdir/mixed.conf"; \
	grep -q 'v3 versiond_legacy' "$$tmpdir/mixed.conf"; \
	grep -q 'versiond_ha_pool "true"' "$$tmpdir/mixed.conf"; \
	grep -q 'proxy_set_header Devshard-Ha' "$$tmpdir/mixed.conf"; \
	grep -q 'proxy_next_upstream error timeout http_502' "$$tmpdir/mixed.conf"; \
	grep -q 'max_fails=1 fail_timeout=10s' "$$tmpdir/mixed.conf"; \
	grep -q 'proxy_connect_timeout 2s' "$$tmpdir/mixed.conf"; \
	VERSIOND_HOSTS='versiond versiond2' \
	VERSIOND_NON_HA_VERSIONS= \
	VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/allha.conf" \
	./entrypoint.sh >/dev/null; \
	grep -q 'default versiond_ha_pool' "$$tmpdir/allha.conf"; \
	! grep -q 'versiond_legacy;' "$$tmpdir/allha.conf" || ! grep -q 'v1 versiond_legacy' "$$tmpdir/allha.conf"; \
	VERSIOND_HOSTS='versiond' \
	VERSIOND_NON_HA_VERSIONS= \
	VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/single.conf" \
	./entrypoint.sh >/dev/null; \
	! grep -q 'versiond_ha_pool "true"' "$$tmpdir/single.conf"; \
	echo "test-render: ok"; \
	rm -rf "$$tmpdir"

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
PLATFORMS ?= linux/amd64,linux/arm64
USE_REGISTRY_CACHE ?= 0
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/gonka-ai/versiond-router:buildcache --cache-to type=registry,ref=ghcr.io/gonka-ai/versiond-router:buildcache,mode=min
DOCKER_BUILD_PREFIX := docker buildx build --load $(DOCKER_CACHE_ARGS)
else
DOCKER_CACHE_ARGS :=
DOCKER_BUILD_PREFIX := DOCKER_BUILDKIT=1 docker build
endif

define DOCKER_BUILD
	@echo "--> building versiond-router docker image"
	@echo "    	platform: $(PLATFORM)"
	@$(DOCKER_BUILD_PREFIX) \
		--platform $(PLATFORM) \
		-f $(DOCKER_FILE) \
		. \
		-t $(DOCKER_TAG)
endef

all: build-docker

build-docker:
	$(eval PLATFORM=linux/amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=ghcr.io/product-science/versiond-router:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/versiond-router:latest; \
	fi

release:
	@echo "--> releasing versiond-router (multi-arch)"
	@echo "platforms: $(PLATFORMS)"
	$(eval DOCKER_FILE=Dockerfile)
	@docker buildx build \
		--platform $(PLATFORMS) \
		-f $(DOCKER_FILE) \
		-t ghcr.io/product-science/versiond-router:$(VERSION) \
		--push \
		.
