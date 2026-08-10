.PHONY: all build-docker release test-render test-routing

HAPROXY_IMAGE ?= haproxy:3.2-alpine

test-render:
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	trap 'rm -rf "$$tmpdir"' EXIT; \
	fail() { echo "test-render: $$1" >&2; exit 1; }; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/proxy.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/versions.map" \
	VERSIOND_NON_HA_VERSIONS='v1 v2 v3' \
	VERSIOND_VERSIONS='v4 v5' \
	./entrypoint.sh; \
	grep -q 'server-template policy 4 proxy-policy:80 .*send-proxy-v2' "$$tmpdir/proxy.cfg" \
		|| fail "public HTTP does not use the PROXY-protocol policy pool"; \
	grep -q 'server-template policy 4 proxy-policy:443 .*send-proxy-v2' "$$tmpdir/proxy.cfg" \
		|| fail "public HTTPS does not use the PROXY-protocol policy pool"; \
	[ "$$(grep -c '^ *option redispatch$$' "$$tmpdir/proxy.cfg")" -ge 3 ] \
		|| fail "connection failures are not redispatched across policy workers"; \
	[ "$$(grep -c 'server-template policy .*init-state up' "$$tmpdir/proxy.cfg")" -eq 2 ] \
		|| fail "resolved policy workers do not start in connect-failover mode"; \
	grep -q '^ *timeout client 86400s$$' "$$tmpdir/proxy.cfg" \
		|| fail "public TCP client idle timeout does not cover upgraded streams"; \
	grep -q '^ *timeout server 86400s$$' "$$tmpdir/proxy.cfg" \
		|| fail "public TCP backend idle timeout does not cover upgraded streams"; \
	grep -q '^v5 versiond_routers_v5$$' "$$tmpdir/versions.map" \
		|| fail "declared versions are not mapped to route-aware router pools"; \
	grep -q '^backend versiond_routers_v5$$' "$$tmpdir/proxy.cfg" \
		|| fail "v5 router backend was not rendered"; \
	grep -q 'http-check send meth GET uri /readyz?version=v5' "$$tmpdir/proxy.cfg" \
		|| fail "router health is not version-aware"; \
	grep -q 'query -m str version=v5.*nbsrv(versiond_routers_v5)' "$$tmpdir/proxy.cfg" \
		|| fail "top-level version readiness is not route-aware"; \
	grep -q 'server-template router 16 versiond-router-fleet:8080 check port 8404' "$$tmpdir/proxy.cfg" \
		|| fail "router data and admin ports are not separated"; \
	grep -q '^ *balance leastconn$$' "$$tmpdir/proxy.cfg" \
		|| fail "long streams are not distributed with leastconn"; \
	grep -q 'retry-on conn-failure empty-response 502' "$$tmpdir/proxy.cfg" \
		|| fail "non-503 connection failover was removed"; \
	grep -q 'disable-l7-retry if !METH_GET !METH_HEAD !METH_OPTIONS' "$$tmpdir/proxy.cfg" \
		|| fail "non-idempotent replay protection was removed"; \
	grep -q 'http-check send meth GET uri /readyz' "$$tmpdir/proxy.cfg" \
		|| fail "edge-api active readiness is absent"; \
	grep -q '^ *bind 127.0.0.1:8404$$' "$$tmpdir/proxy.cfg" \
		|| fail "admin listener is not loopback-only"; \
	if grep -q 'set-header X-Real-IP' "$$tmpdir/proxy.cfg"; then \
		fail "parent distributor overwrites policy-derived client IP"; \
	fi; \
	docker run --rm --user "$$(id -u):$$(id -g)" \
		-v "$$tmpdir:$$tmpdir:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f "$$tmpdir/proxy.cfg" >/dev/null \
		|| fail "rendered proxy config is invalid"; \
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/invalid.cfg" \
		PROXY_ROUTER_VERSION_MAP="$$tmpdir/invalid.map" \
		VERSIOND_NON_HA_VERSIONS=v4 VERSIOND_VERSIONS=v4 \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "duplicate version declaration was accepted"; \
	fi; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/both.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/both.map" \
	NGINX_MODE=both VERSIOND_VERSIONS=v5 \
	./entrypoint.sh; \
	[ "$$(grep -c 'path /readyz.*!{ query -m found }.*nbsrv(policy_http).*nbsrv(policy_https)' "$$tmpdir/both.cfg")" -eq 1 ] \
		|| fail "both-mode readiness does not require both policy pools"; \
	[ "$$(grep -c 'path /readyz.*!{ query -m found }.*status 503\|return status 503.*path /readyz.*!{ query -m found }' "$$tmpdir/both.cfg")" -eq 1 ] \
		|| fail "both-mode readiness has an ambiguous generic fallback"; \
	echo "test-render: ok"

test-routing:
	@./test-routing.sh

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
PLATFORMS ?= linux/amd64,linux/arm64

all: build-docker

build-docker:
	@echo "--> building proxy-router docker image"
	@DOCKER_BUILDKIT=1 docker build \
		--platform $(DOCKER_PLATFORM) \
		-f Dockerfile \
		-t ghcr.io/product-science/proxy-router:$(VERSION) .
	@if [ "$(SET_LATEST)" = "1" ]; then \
		docker tag ghcr.io/product-science/proxy-router:$(VERSION) \
			ghcr.io/product-science/proxy-router:latest; \
		docker tag ghcr.io/product-science/proxy-router:$(VERSION) proxy-router:latest; \
	fi

release:
	@echo "--> releasing proxy-router (multi-arch)"
	@docker buildx build \
		--platform $(PLATFORMS) \
		-f Dockerfile \
		-t ghcr.io/product-science/proxy-router:$(VERSION) \
		--push .
