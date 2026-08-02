.PHONY: all build-docker release test-render

# Renders both the mixed (legacy pinning) and the all-HA shapes, asserts on the
# result, then runs the real HAProxy from the production image over each one.
# Rendering needs no HAProxy locally; the syntax check needs Docker.
HAPROXY_IMAGE ?= haproxy:3.0-alpine

test-render:
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	trap 'rm -rf "$$tmpdir"' EXIT; \
	fail() { echo "test-render: $$1" >&2; exit 1; }; \
	HAPROXY_BIN=true \
	VERSIOND_ROUTER_RENDER_ONLY=1 \
	VERSIOND_POOL_HOST=versiond-pool \
	VERSIOND_LEGACY_HOST=versiond \
	VERSIOND_NON_HA_VERSIONS='v1,v2 v3' \
	GONKA_HA=true \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/mixed.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/mixed.map" \
	./entrypoint.sh; \
	grep -q 'server-template versiond 64 versiond-pool:8080' "$$tmpdir/mixed.cfg" \
		|| fail "HA pool is not a DNS-resolved server-template"; \
	grep -q 'hash-type consistent' "$$tmpdir/mixed.cfg" \
		|| fail "HA pool lost consistent hashing"; \
	grep -q 'http-check send meth GET uri /readyz' "$$tmpdir/mixed.cfg" \
		|| fail "active health checks are not configured"; \
	grep -q 'http-request set-header Devshard-Ha true' "$$tmpdir/mixed.cfg" \
		|| fail "GONKA_HA did not enable the Devshard-Ha header"; \
	grep -q 'server legacy versiond:8080' "$$tmpdir/mixed.cfg" \
		|| fail "legacy backend does not point at VERSIOND_LEGACY_HOST"; \
	grep -q 'retry-on conn-failure empty-response 502' "$$tmpdir/mixed.cfg" \
		|| fail "failover no longer excludes 503"; \
	grep -q 'disable-l7-retry if !METH_GET' "$$tmpdir/mixed.cfg" \
		|| fail "non-idempotent requests could be replayed"; \
	grep -q 'bind 127.0.0.1:8405' "$$tmpdir/mixed.cfg" \
		|| fail "metrics are not bound to loopback"; \
	printf 'v1 legacy\nv2 legacy\nv3 legacy\n' > "$$tmpdir/want.map"; \
	diff -u "$$tmpdir/want.map" "$$tmpdir/mixed.map" \
		|| fail "pinned versions rendered incorrectly"; \
	HAPROXY_BIN=true \
	VERSIOND_ROUTER_RENDER_ONLY=1 \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/allha.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/allha.map" \
	./entrypoint.sh; \
	[ ! -s "$$tmpdir/allha.map" ] \
		|| fail "no pinned versions were requested but the map is not empty"; \
	grep -q 'http-request del-header Devshard-Ha' "$$tmpdir/allha.cfg" \
		|| fail "unset GONKA_HA must strip a client-supplied Devshard-Ha"; \
	if grep -q 'set-header Devshard-Ha' "$$tmpdir/allha.cfg"; then \
		fail "unset GONKA_HA still advertises an HA deployment"; \
	fi; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
		VERSIOND_ROUTER_STREAM_IDLE_SECONDS=1200 \
		VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS=60 \
		./entrypoint.sh 2>/dev/null; then \
		fail "a tunnel timeout below the idle timeout was accepted"; \
	fi; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
		VERSIOND_PORT=invalid \
		./entrypoint.sh 2>/dev/null; then \
		fail "a non-numeric port was accepted"; \
	fi; \
	for cfg in mixed allha; do \
		docker run --rm --user "$$(id -u):$$(id -g)" -v "$$tmpdir:$$tmpdir:ro" $(HAPROXY_IMAGE) \
			haproxy -c -f "$$tmpdir/$$cfg.cfg" >/dev/null \
			|| fail "$$cfg.cfg is not valid HAProxy configuration"; \
	done; \
	echo "test-render: ok"

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
