.PHONY: all build-docker release test-render test-readiness-compat test-local-compose

HAPROXY_IMAGE ?= haproxy:3.2-alpine

test-local-compose:
	@set -eu; \
	tmp=$$(mktemp); \
	trap 'rm -f "$$tmp"' EXIT; \
	KEY_NAME=testnet VERSIOND_SIGNER_KEY_NAME=test \
		docker compose --project-directory .. \
		-f ../local-test-net/docker-compose-base.yml \
		-f ../local-test-net/docker-compose.edge-api.yml config > "$$tmp"; \
	grep -q 'EDGE_API_POOL_HOST: testnet-edge-api-pool' "$$tmp" \
		|| { echo 'test-local-compose: pool host is not rendered' >&2; exit 1; }; \
	! grep -q 'EDGE_API_HOSTS' "$$tmp" \
		|| { echo 'test-local-compose: stale static host list remains' >&2; exit 1; }; \
	[ "$$(grep -c -- '- testnet-edge-api-pool' "$$tmp")" -ge 3 ] \
		|| { echo 'test-local-compose: not every edge-api joins the pool alias' >&2; exit 1; }; \
	grep -Eq 'context: .*/edge-api-router$$' "$$tmp" \
		|| { echo 'test-local-compose: router build context is wrong' >&2; exit 1; }; \
	echo 'test-local-compose: ok'

test-readiness-compat:
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	net=gonka-edge-readiness-probe; \
	cleanup() { \
		docker rm -f gonka-er-router gonka-er-legacy gonka-er-ready gonka-er-draining \
			>/dev/null 2>&1 || true; \
		docker network rm $$net >/dev/null 2>&1 || true; \
		rm -rf "$$tmpdir"; \
	}; \
	trap cleanup EXIT; \
	fail() { \
		for container in gonka-er-router gonka-er-legacy gonka-er-ready gonka-er-draining; do \
			echo "--- $$container" >&2; docker logs $$container >&2 || true; \
		done; \
		echo "test-readiness-compat: $$1" >&2; exit 1; \
	}; \
	printf '%s\n' \
		'import os, http.server' \
		'NAME = os.environ["NAME"]' \
		'READYZ = int(os.environ["READYZ"])' \
		'class H(http.server.BaseHTTPRequestHandler):' \
		'    protocol_version = "HTTP/1.1"' \
		'    def reply(self, code, body):' \
		'        data = body.encode()' \
		'        self.send_response(code)' \
		'        self.send_header("Content-Length", str(len(data)))' \
		'        self.end_headers()' \
		'        self.wfile.write(data)' \
		'    def do_GET(self):' \
		'        if self.path == "/readyz": return self.reply(READYZ, NAME)' \
		'        if self.path == "/healthz": return self.reply(200, "ok")' \
		'        self.reply(200, NAME)' \
		'    def log_message(self, *args): pass' \
		'http.server.ThreadingHTTPServer(("", 18080), H).serve_forever()' \
		> "$$tmpdir/upstream.py"; \
	docker network create $$net >/dev/null; \
	docker build -q -t gonka-edge-api-router-probe . >/dev/null; \
	for spec in legacy:404 ready:200 draining:503; do \
		name=$${spec%%:*}; status=$${spec##*:}; \
		docker run -d --name gonka-er-$$name --network $$net \
			--network-alias edge-api-pool -e NAME=$$name -e READYZ=$$status \
			-v "$$tmpdir/upstream.py:/app.py:ro" \
			python:3.12-alpine python /app.py >/dev/null; \
	done; \
	docker run -d --name gonka-er-router --network $$net -p 127.0.0.1:18133:18080 \
		-e EDGE_API_POOL_HOST=edge-api-pool gonka-edge-api-router-probe >/dev/null; \
	for i in $$(seq 60); do \
		if curl -fsS -m 2 http://127.0.0.1:18133/probe > "$$tmpdir/first" 2>/dev/null; then break; fi; \
		sleep 0.25; \
	done; \
	# A first ready server can satisfy the probe while another server still needs \
	# its second passing check (`rise 2`). Wait for one complete extra cycle. \
	sleep 3; \
	: > "$$tmpdir/results"; \
	for i in $$(seq 40); do curl -fsS -m 2 http://127.0.0.1:18133/probe >> "$$tmpdir/results"; echo >> "$$tmpdir/results"; done; \
	grep -qx legacy "$$tmpdir/results" \
		|| fail "legacy /healthz backend was not admitted; responses: $$(tr '\n' ' ' < "$$tmpdir/results")"; \
	grep -qx ready "$$tmpdir/results" \
		|| fail "ready backend was not admitted; responses: $$(tr '\n' ' ' < "$$tmpdir/results")"; \
	if grep -qx draining "$$tmpdir/results"; then fail "503 /readyz backend received traffic"; fi; \
	echo "test-readiness-compat: ok"

test-render: test-readiness-compat test-local-compose
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
	grep -q '^[[:space:]]*server-template edgeapi 64 edge-api-pool:18080' "$$tmpdir/edge.cfg" \
		|| fail "pool is not a DNS-resolved server-template"; \
	grep -q '^[[:space:]]*balance roundrobin' "$$tmpdir/edge.cfg" \
		|| fail "stateless pool should round-robin"; \
	grep -q '^[[:space:]]*http-check send meth GET uri /readyz' "$$tmpdir/edge.cfg" \
		|| fail "active health checks are not configured"; \
	grep -q '^[[:space:]]*http-check expect status 200,404' "$$tmpdir/edge.cfg" \
		|| fail "legacy readiness compatibility is not configured"; \
	grep -q '^[[:space:]]*http-check send meth GET uri /healthz' "$$tmpdir/edge.cfg" \
		|| fail "legacy liveness is not verified"; \
	grep -q '^[[:space:]]*option[[:space:]]*forwardfor' "$$tmpdir/edge.cfg" \
		|| fail "X-Forwarded-For is no longer appended"; \
	grep -q '^[[:space:]]*http-request set-header X-Real-IP' "$$tmpdir/edge.cfg" \
		|| fail "X-Real-IP is no longer set"; \
	grep -Eq '^[[:space:]]*timeout check 3s([[:space:]]|$$)' "$$tmpdir/edge.cfg" \
		|| fail "no timeout check directive: the readiness check needs its own budget"; \
	grep -Eq '^[[:space:]]*accepted_payload_size 8192([[:space:]]|$$)' "$$tmpdir/edge.cfg" \
		|| fail "DNS replies are still capped below the configured pool capacity"; \
	grep -q '^[[:space:]]*retry-on conn-failure empty-response 502' "$$tmpdir/edge.cfg" \
		|| fail "failover no longer excludes 503"; \
	grep -q '^[[:space:]]*bind 127.0.0.1:8405' "$$tmpdir/edge.cfg" \
		|| fail "metrics are not bound to loopback"; \
	for badhost in 'a&b' 'a|b'; do \
		if HAPROXY_BIN=true EDGE_API_ROUTER_RENDER_ONLY=1 \
			EDGE_API_POOL_HOST="$$badhost" \
			EDGE_API_ROUTER_TEMPLATE=./haproxy.cfg.template \
			EDGE_API_ROUTER_OUT="$$tmpdir/bad.cfg" \
			./entrypoint.sh 2>/dev/null; then \
			fail "hostname '$$badhost' would be mangled by the renderer and was accepted"; \
		fi; \
	done; \
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
