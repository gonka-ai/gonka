.PHONY: all build-docker release test-render

HAPROXY_IMAGE ?= haproxy:3.2-alpine

test-render:
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	trap 'rm -rf "$$tmpdir"' EXIT; \
	fail() { echo "test-render: $$1" >&2; exit 1; }; \
	HAPROXY_BIN=true \
	EDGE_API_ROUTER_RENDER_ONLY=1 \
	EDGE_API_POOL_HOST=edge-api-pool \
	EDGE_API_ROUTER_TEMPLATE=./haproxy.cfg.template \
	EDGE_API_ROUTER_OUT="$$tmpdir/edge.cfg" \
	./entrypoint.sh; \
	grep -q 'server-template edgeapi 64 edge-api-pool:18080' "$$tmpdir/edge.cfg" \
		|| fail "pool is not a DNS-resolved server-template"; \
	grep -q 'balance roundrobin' "$$tmpdir/edge.cfg" \
		|| fail "stateless pool should round-robin"; \
	grep -q 'http-check send meth GET uri /readyz' "$$tmpdir/edge.cfg" \
		|| fail "active health checks are not configured"; \
	grep -q 'timeout check' "$$tmpdir/edge.cfg" \
		|| fail "no timeout check: the check budget must exceed edge-api's 2s chain probe"; \
	grep -q 'retry-on conn-failure empty-response 502' "$$tmpdir/edge.cfg" \
		|| fail "failover no longer excludes 503"; \
	grep -q 'bind 127.0.0.1:8405' "$$tmpdir/edge.cfg" \
		|| fail "metrics are not bound to loopback"; \
	if HAPROXY_BIN=true EDGE_API_ROUTER_RENDER_ONLY=1 \
		EDGE_API_ROUTER_TEMPLATE=./haproxy.cfg.template \
		EDGE_API_ROUTER_OUT="$$tmpdir/bad.cfg" \
		EDGE_API_PORT=invalid \
		./entrypoint.sh 2>/dev/null; then \
		fail "a non-numeric port was accepted"; \
	fi; \
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$$tmpdir:/cfg:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f /cfg/edge.cfg >/dev/null \
		|| fail "edge.cfg is not valid HAProxy configuration"; \
	echo "test-render: ok"

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
PLATFORMS ?= linux/amd64,linux/arm64
USE_REGISTRY_CACHE ?= 0
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/gonka-ai/edge-api-router:buildcache --cache-to type=registry,ref=ghcr.io/gonka-ai/edge-api-router:buildcache,mode=min
DOCKER_BUILD_PREFIX := docker buildx build --load $(DOCKER_CACHE_ARGS)
else
DOCKER_CACHE_ARGS :=
DOCKER_BUILD_PREFIX := DOCKER_BUILDKIT=1 docker build
endif

define DOCKER_BUILD
	@echo "--> building edge-api-router docker image"
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
	$(eval DOCKER_TAG=ghcr.io/product-science/edge-api-router:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/edge-api-router:latest; \
		docker tag $(DOCKER_TAG) edge-api-router:latest; \
	fi

release:
	@echo "--> releasing edge-api-router (multi-arch)"
	@echo "platforms: $(PLATFORMS)"
	$(eval DOCKER_FILE=Dockerfile)
	@docker buildx build \
		--platform $(PLATFORMS) \
		-f $(DOCKER_FILE) \
		-t ghcr.io/product-science/edge-api-router:$(VERSION) \
		--push \
		.
