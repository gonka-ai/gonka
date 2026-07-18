.PHONY: all build-docker build-tools release test test-render

TOOLS_DIR ?= $(CURDIR)/.bin

build-tools:
	mkdir -p $(TOOLS_DIR)
	go build -o $(TOOLS_DIR)/gonka-routerctl ./cmd/gonka-routerctl
	go build -o $(TOOLS_DIR)/gonka-hostctl ./cmd/gonka-hostctl

test:
	go test ./...

test-render:
	@tmpdir=$$(mktemp -d); \
	VERSIOND_HOSTS='versiond versiond2' \
	VERSIOND_LEGACY_HOST=versiond \
	VERSIOND_NON_HA_VERSIONS='v1,v2 v3' \
	VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/mixed.conf" \
	VERSIOND_ROUTER_STATE="$$tmpdir/mixed-state.json" \
	VERSIOND_ROUTER_AUDIT="$$tmpdir/mixed-audit.jsonl" \
	VERSIOND_ROUTER_LOCK="$$tmpdir/mixed.lock" \
	VERSIOND_ROUTER_NGINX_BIN=true \
	go run ./cmd/gonka-routerctl bootstrap >/dev/null; \
	grep -q 'default versiond_ha_pool' "$$tmpdir/mixed.conf"; \
	grep -q 'v1 versiond_legacy' "$$tmpdir/mixed.conf"; \
	grep -q 'v2 versiond_legacy' "$$tmpdir/mixed.conf"; \
	grep -q 'v3 versiond_legacy' "$$tmpdir/mixed.conf"; \
	grep -q 'versiond_ha_pool "true"' "$$tmpdir/mixed.conf"; \
	grep -q 'proxy_set_header Devshard-Ha' "$$tmpdir/mixed.conf"; \
	grep -q 'proxy_next_upstream error timeout http_502' "$$tmpdir/mixed.conf"; \
	grep -q 'max_fails=1 fail_timeout=10s' "$$tmpdir/mixed.conf"; \
	grep -q 'proxy_connect_timeout 2s' "$$tmpdir/mixed.conf"; \
	VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/mixed.conf" \
	VERSIOND_ROUTER_STATE="$$tmpdir/mixed-state.json" \
	VERSIOND_ROUTER_AUDIT="$$tmpdir/mixed-audit.jsonl" \
	VERSIOND_ROUTER_LOCK="$$tmpdir/mixed.lock" \
	VERSIOND_ROUTER_NGINX_BIN=true \
	go run ./cmd/gonka-routerctl host drain --operation-id test-drain versiond2 >/dev/null; \
	grep -q 'server versiond2:8080 resolve max_fails=1 fail_timeout=10s down;' "$$tmpdir/mixed.conf"; \
	VERSIOND_HOSTS='versiond versiond2' \
	VERSIOND_NON_HA_VERSIONS= \
	VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/allha.conf" \
	VERSIOND_ROUTER_STATE="$$tmpdir/allha-state.json" \
	VERSIOND_ROUTER_AUDIT="$$tmpdir/allha-audit.jsonl" \
	VERSIOND_ROUTER_LOCK="$$tmpdir/allha.lock" \
	VERSIOND_ROUTER_NGINX_BIN=true \
	go run ./cmd/gonka-routerctl bootstrap >/dev/null; \
	grep -q 'default versiond_ha_pool' "$$tmpdir/allha.conf"; \
	! grep -q 'versiond_legacy;' "$$tmpdir/allha.conf" || ! grep -q 'v1 versiond_legacy' "$$tmpdir/allha.conf"; \
	VERSIOND_HOSTS='versiond' \
	VERSIOND_NON_HA_VERSIONS= \
	VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/single.conf" \
	VERSIOND_ROUTER_STATE="$$tmpdir/single-state.json" \
	VERSIOND_ROUTER_AUDIT="$$tmpdir/single-audit.jsonl" \
	VERSIOND_ROUTER_LOCK="$$tmpdir/single.lock" \
	VERSIOND_ROUTER_NGINX_BIN=true \
	go run ./cmd/gonka-routerctl bootstrap >/dev/null; \
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
