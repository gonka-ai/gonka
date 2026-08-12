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
	HAPROXY_DNS_RESOLVER=10.96.0.10:53 \
	PROXY_ROUTER_CATALOG_BIND_HOST=localhost \
	PROXY_ROUTER_CATALOG_UPSTREAM_HOST=api \
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
	grep -q '^version=v5 versiond_routers_v5$$' "$$tmpdir/versions.map" \
		|| fail "data routing and readiness are not rendered in one projection"; \
	grep -q '^backend versiond_routers_v5$$' "$$tmpdir/proxy.cfg" \
		|| fail "v5 router backend was not rendered"; \
	[ "$$(grep -c '^backend versiond_routers_dynamic_' "$$tmpdir/proxy.cfg")" -eq 32 ] \
		|| fail "the default dynamic governance capacity is not pre-rendered"; \
	[ "$$(grep -c '^versiond_routers_dynamic_.* __unassigned__$$' \
		"$$tmpdir/proxy.cfg.version-slots.map")" -eq 32 ] \
		|| fail "dynamic distributor slots are not initialized as unassigned"; \
	awk '/^backend versiond_routers_dynamic_1$$/{inside=1; next} /^backend /{inside=0} \
		inside && /server-template .* disabled$$/{found=1} END{exit !found}' \
		"$$tmpdir/proxy.cfg" \
		|| fail "unused distributor slots are performing health checks"; \
	grep -q 'stats socket /var/run/haproxy/reconciler.sock mode 600 level admin' \
		"$$tmpdir/proxy.cfg" \
		|| fail "the catalog writer does not have a dedicated restricted socket"; \
	grep -q 'http-check send meth GET uri /readyz?version=v5' "$$tmpdir/proxy.cfg" \
		|| fail "router health is not version-aware"; \
	grep -q 'query -m str version=v5.*nbsrv(versiond_routers_v5)' "$$tmpdir/proxy.cfg" \
		|| fail "top-level version readiness is not route-aware"; \
	grep -q 'server-template router 16 versiond-router-fleet:8080 check port 8404' "$$tmpdir/proxy.cfg" \
		|| fail "router data and admin ports are not separated"; \
	grep -q '^ *balance hash var(txn.hashkey)$$' "$$tmpdir/proxy.cfg" \
		|| fail "router selection is not sticky by the canonical escrow key"; \
	grep -q '^ *hash-type consistent$$' "$$tmpdir/proxy.cfg" \
		|| fail "router pools lost consistent hashing"; \
	grep -q 'set-var(txn.hashkey).*field(3,/).*sessions' "$$tmpdir/proxy.cfg" \
		|| fail "versionless session routes do not reuse the escrow routing key"; \
	grep -q '^ *nameserver dns 10.96.0.10:53$$' "$$tmpdir/proxy.cfg" \
		|| fail "the configured DNS resolver was not rendered"; \
	grep -q '^frontend routing_catalog$$' "$$tmpdir/proxy.cfg" \
		|| fail "the read-only catalog bridge was not rendered"; \
	grep -q '^ *bind 127.0.0.1:9100$$' "$$tmpdir/proxy.cfg" \
		|| fail "the catalog bridge is not bound to its isolated interface"; \
	grep -q 'return status 405.*unless METH_GET' "$$tmpdir/proxy.cfg" \
		|| fail "the catalog bridge accepts mutating methods"; \
	grep -q 'return status 404.*unless { path /versions }' "$$tmpdir/proxy.cfg" \
		|| fail "the catalog bridge exposes paths other than /versions"; \
	grep -q 'server catalog api:9100 check' "$$tmpdir/proxy.cfg" \
		|| fail "the catalog bridge does not target dapi"; \
	grep -q 'retry-on conn-failure empty-response 502' "$$tmpdir/proxy.cfg" \
		|| fail "non-503 connection failover was removed"; \
	awk '/^backend versiond_router_coarse$$/{inside=1; next} /^backend /{inside=0} \
		inside && /retry-on conn-failure empty-response 502 404/{found=1} END{exit !found}' \
		"$$tmpdir/proxy.cfg" \
		|| fail "versionless GET requests cannot retry a route-local 404"; \
	awk '/^backend versiond_routers_v5$$/{inside=1; next} /^backend /{inside=0} \
		inside && /retry-on conn-failure empty-response 502$$/{found=1} \
		inside && /retry-on .* 404/{bad=1} END{exit !found || bad}' "$$tmpdir/proxy.cfg" \
		|| fail "explicit version backends retry authoritative 404 responses"; \
	grep -q 'disable-l7-retry if !METH_GET !METH_HEAD !METH_OPTIONS' "$$tmpdir/proxy.cfg" \
		|| fail "non-idempotent replay protection was removed"; \
	grep -q 'http-check send meth GET uri /readyz' "$$tmpdir/proxy.cfg" \
		|| fail "edge-api active readiness is absent"; \
	grep -q '^ *bind 127.0.0.1:8404$$' "$$tmpdir/proxy.cfg" \
		|| fail "admin listener is not loopback-only"; \
	if grep -q 'set-header X-Real-IP' "$$tmpdir/proxy.cfg"; then \
		fail "parent distributor overwrites policy-derived client IP"; \
	fi; \
	if grep -Eq 'X-Edge-API-Upstream|X-Versiond-Router-Addr' "$$tmpdir/proxy.cfg"; then \
		fail "public responses expose an internal upstream address"; \
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
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/invalid-catalog.cfg" \
		VERSIOND_ROUTING_CATALOG_URL=unix:///tmp/catalog \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "an unsupported catalog URL scheme was accepted"; \
	fi; \
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/invalid-catalog.cfg" \
		VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS=0 \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "a zero catalog fetch timeout was accepted"; \
	fi; \
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/invalid-dns.cfg" \
		HAPROXY_DNS_RESOLVER='dns service:53' \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "an unsafe DNS resolver token was accepted"; \
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
		-t ghcr.io/product-science/proxy-router:$(VERSION) ..
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
		--push ..
