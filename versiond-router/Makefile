.PHONY: all build-docker release test-render
.PHONY: test-catalog-cache test-hash-ring test-version-routing test-local-compose test-join-compose

# Renders both the mixed (legacy pinning) and the all-HA shapes, asserts on the
# result, then runs the real HAProxy from the production image over each one.
# Rendering needs no HAProxy locally; the syntax check needs Docker.
HAPROXY_IMAGE ?= haproxy:3.2-alpine

test-local-compose:
	@set -eu; \
	tmp=$$(mktemp); tmp_json=$$(mktemp); \
	trap 'rm -f "$$tmp" "$$tmp_json"' EXIT; \
	KEY_NAME=testnet VERSIOND_SIGNER_KEY_NAME=test \
		docker compose --project-directory .. \
		-f ../local-test-net/docker-compose-base.yml \
		-f ../local-test-net/docker-compose.versiond.yml config > "$$tmp"; \
	KEY_NAME=testnet VERSIOND_SIGNER_KEY_NAME=test \
		docker compose --project-directory .. \
		-f ../local-test-net/docker-compose-base.yml \
		-f ../local-test-net/docker-compose.versiond.yml config --format json > "$$tmp_json"; \
	repo_root=$$(CDPATH='' cd -- .. && pwd); \
	jq -e --arg root "$$repo_root" \
		'.services["versiond-router"].build.context == $$root and .services["versiond-router"].build.dockerfile == "versiond-router/Dockerfile"' \
		"$$tmp_json" >/dev/null \
		|| { echo 'test-local-compose: router build context is not the repository root' >&2; exit 1; }; \
	grep -q 'VERSIOND_POOL_HOST: testnet-versiond-pool' "$$tmp" \
		|| { echo 'test-local-compose: pool host is not rendered' >&2; exit 1; }; \
	! grep -q 'VERSIOND_HOSTS' "$$tmp" \
		|| { echo 'test-local-compose: stale static host list remains' >&2; exit 1; }; \
	[ "$$(grep -c -- '- testnet-versiond-pool' "$$tmp")" -ge 3 ] \
		|| { echo 'test-local-compose: not every versiond joins the pool alias' >&2; exit 1; }; \
	grep -q 'stop_signal: SIGUSR1' "$$tmp" \
		|| { echo 'test-local-compose: router soft-stop signal is missing' >&2; exit 1; }; \
	echo 'test-local-compose: ok'

test-join-compose:
	@set -eu; \
	tmp=$$(mktemp); tmp_json=$$(mktemp); \
	trap 'rm -f "$$tmp" "$$tmp_json"' EXIT; \
	VERSIOND_ROUTER_IMAGE= VERSIOND_ROUTING_CATALOG_URL= \
	DEVSHARD_POSTGRES_PASSWORD=test \
		docker compose --project-directory ../deploy/join \
		-f ../deploy/join/docker-compose.yml \
		-f ../deploy/join/docker-compose.versiond.yml config > "$$tmp"; \
	VERSIOND_ROUTER_IMAGE= VERSIOND_ROUTING_CATALOG_URL= \
	DEVSHARD_POSTGRES_PASSWORD=test \
		docker compose --project-directory ../deploy/join \
		-f ../deploy/join/docker-compose.yml \
		-f ../deploy/join/docker-compose.versiond.yml config --format json > "$$tmp_json"; \
	jq -e '.services["versiond-router"].volumes | any(.type == "volume" and .source == "versiond-router-state" and .target == "/var/lib/gonka-router")' \
		"$$tmp_json" >/dev/null \
		|| { echo 'test-join-compose: router catalog state is not persistent' >&2; exit 1; }; \
	jq -e '.services["versiond-router"].healthcheck.test | join(" ") | contains("readyz?component=catalog")' \
		"$$tmp_json" >/dev/null \
		|| { echo 'test-join-compose: router healthcheck does not expose catalog convergence' >&2; exit 1; }; \
	grep -q 'VERSIOND_POOL_HOST: versiond-pool' "$$tmp" \
		|| { echo 'test-join-compose: pool host is not rendered' >&2; exit 1; }; \
	[ "$$(grep -c -- '- versiond-pool' "$$tmp")" -ge 2 ] \
		|| { echo 'test-join-compose: not every versiond joins the pool alias' >&2; exit 1; }; \
	grep -q 'VERSIOND_HOSTS: versiond versiond2' "$$tmp" \
		|| { echo 'test-join-compose: legacy router compatibility was lost' >&2; exit 1; }; \
	[ "$$(grep -c 'GONKA_HA: "true"' "$$tmp")" -eq 3 ] \
		|| { echo 'test-join-compose: HA deployment latch is not shared by both versiond hosts and the router' >&2; exit 1; }; \
	[ "$$(grep -c 'VERSIOND_NON_HA_VERSIONS: v1 v2 v3' "$$tmp")" -eq 3 ] \
		|| { echo 'test-join-compose: legacy pins are not shared by both versiond hosts and the router' >&2; exit 1; }; \
	grep -Eq '^[[:space:]]*VERSIOND_VERSIONS: v4 v5$$' "$$tmp" \
		|| { echo 'test-join-compose: per-version routing contract is missing' >&2; exit 1; }; \
	grep -q 'image: ghcr.io/product-science/versiond-router:0.2.15' "$$tmp" \
		|| { echo 'test-join-compose: published legacy router default changed' >&2; exit 1; }; \
	grep -q 'VERSIOND_ROUTING_CATALOG_URL: ""' "$$tmp" \
		|| { echo 'test-join-compose: catalog was enabled on the legacy router image' >&2; exit 1; }; \
	grep -q 'VERSIOND_ROUTING_ACTIVATION_MIN_READY: "2"' "$$tmp" \
		|| { echo 'test-join-compose: two-replica catalog reserve is not explicit' >&2; exit 1; }; \
	grep -q 'VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS: "false"' "$$tmp" \
		|| { echo 'test-join-compose: automatic catalog removals are not disabled' >&2; exit 1; }; \
	grep -q 'VERSIOND_NON_HA_VERSIONS: v1 v2 v3' "$$tmp" \
		|| { echo 'test-join-compose: legacy version pins are missing' >&2; exit 1; }; \
	VERSIOND_ROUTER_IMAGE=ghcr.io/product-science/versiond-router@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
	VERSIOND_ROUTING_CATALOG_URL=http://api:9100/versions \
	VERSIOND_ROUTING_CATALOG_POLL_SECONDS=7 \
	VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS=11 \
	VERSIOND_ROUTING_CATALOG_MAX_BYTES=123456 \
	VERSIOND_ROUTING_CATALOG_RUNTIME_TIMEOUT_SECONDS=13 \
	VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS=17 \
	VERSIOND_ROUTING_ACTIVATION_MIN_READY=1 \
	VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=true \
	VERSIOND_ROUTER_VERSION_CAPACITY=19 \
	DEVSHARD_POSTGRES_PASSWORD=test \
		docker compose --project-directory ../deploy/join \
		-f ../deploy/join/docker-compose.yml \
		-f ../deploy/join/docker-compose.versiond.yml config > "$$tmp"; \
	grep -q 'image: ghcr.io/product-science/versiond-router@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "$$tmp" \
		|| { echo 'test-join-compose: release router image override was ignored' >&2; exit 1; }; \
	grep -q 'VERSIOND_ROUTING_CATALOG_URL: http://api:9100/versions' "$$tmp" \
		|| { echo 'test-join-compose: release catalog URL override was ignored' >&2; exit 1; }; \
	for expected in \
		'VERSIOND_ROUTING_CATALOG_POLL_SECONDS: "7"' \
		'VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS: "11"' \
		'VERSIOND_ROUTING_CATALOG_MAX_BYTES: "123456"' \
		'VERSIOND_ROUTING_CATALOG_RUNTIME_TIMEOUT_SECONDS: "13"' \
		'VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS: "17"' \
		'VERSIOND_ROUTING_ACTIVATION_MIN_READY: "1"' \
		'VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS: "true"' \
		'VERSIOND_ROUTER_VERSION_CAPACITY: "19"'; do \
		grep -q "$$expected" "$$tmp" \
			|| { echo "test-join-compose: override '$$expected' was ignored" >&2; exit 1; }; \
	done; \
	GONKA_HA=off VERSIOND_NON_HA_VERSIONS= VERSIOND_VERSIONS= \
	VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true \
	DEVSHARD_POSTGRES_PASSWORD=test \
		docker compose --project-directory ../deploy/join \
		-f ../deploy/join/docker-compose.yml \
		-f ../deploy/join/docker-compose.versiond.yml config > "$$tmp"; \
	grep -q 'VERSIOND_NON_HA_VERSIONS: ""' "$$tmp" \
		|| { echo 'test-join-compose: explicit empty legacy pins were replaced by defaults' >&2; exit 1; }; \
	grep -q 'VERSIOND_VERSIONS: ""' "$$tmp" \
		|| { echo 'test-join-compose: explicit empty version list was replaced by defaults' >&2; exit 1; }; \
	grep -q 'VERSIOND_ROUTER_ALLOW_COARSE_READINESS: "true"' "$$tmp" \
		|| { echo 'test-join-compose: coarse-readiness opt-in was not forwarded' >&2; exit 1; }; \
	[ "$$(grep -Ec "GONKA_HA: ['\"]off['\"]" "$$tmp")" -eq 3 ] \
		|| { echo 'test-join-compose: explicit GONKA_HA value diverged between versiond and router' >&2; exit 1; }; \
	echo 'test-join-compose: ok'

test-catalog-cache:
	@../router-runtime/catalog-cache_test.sh

# Which versiond serves an escrow must depend on who is in the pool, not on the
# order DNS happened to hand the addresses to server-template. That is invisible
# in the config and catastrophic when wrong — a router restart would re-home
# every session — so it is checked by running the real thing.
#
# The probe reuses the hashing directives from the rendered config, so dropping
# `hash-key addr` from the template makes this fail rather than silently pass.
# It needs no upstreams: HAProxy logs which server it picked even when the
# connection is refused, and that choice is the only thing under test.
test-hash-ring:
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	trap 'rm -rf "$$tmpdir"; docker rm -f gonka-hash-probe >/dev/null 2>&1 || true' EXIT; \
	fail() { echo "test-hash-ring: $$1" >&2; exit 1; }; \
	HAPROXY_BIN=true \
	VERSIOND_ROUTER_RENDER_ONLY=1 \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/rendered.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/rendered.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/rendered.versions.map" \
	./entrypoint.sh; \
	hashvars=$$(grep 'set-var(txn\.' "$$tmpdir/rendered.cfg"); \
	balance=$$(grep -m1 '^ *balance ' "$$tmpdir/rendered.cfg"); \
	hashtype=$$(grep -m1 '^ *hash-type ' "$$tmpdir/rendered.cfg"); \
	hashkey=$$(grep '^ *server-template ' "$$tmpdir/rendered.cfg" \
		| grep -m1 -o 'hash-key [a-z-]*' || true); \
	if [ -z "$$balance" ] || [ -z "$$hashtype" ] || [ -z "$$hashvars" ]; then \
		fail "could not find the pool's hashing directives in the rendered config"; \
	fi; \
	probe() { \
		{ \
			echo "global"; \
			echo "    log stdout format raw local0 info"; \
			echo "defaults"; \
			echo "    mode http"; \
			echo "    timeout connect 200ms"; \
			echo "    timeout client 5s"; \
			echo "    timeout server 5s"; \
			echo "frontend probe_in"; \
			echo "    bind :8080"; \
			echo "    log global"; \
			echo '    log-format "PICK %HU %si"'; \
			echo "$$hashvars"; \
			echo "    default_backend probe_pool"; \
			echo "backend probe_pool"; \
			echo "$$balance"; \
			echo "$$hashtype"; \
			echo "    retries 0"; \
			i=1; \
			for ip in $$1; do \
				echo "    server versiond$$i $$ip:9 $$hashkey"; \
				i=$$((i + 1)); \
			done; \
		} > "$$tmpdir/probe.cfg"; \
		docker rm -f gonka-hash-probe >/dev/null 2>&1 || true; \
		docker run -d --name gonka-hash-probe -p 127.0.0.1:18131:8080 \
			--user "$$(id -u):$$(id -g)" -v "$$tmpdir:$$tmpdir:ro" \
			$(HAPROXY_IMAGE) haproxy -W -db -f "$$tmpdir/probe.cfg" >/dev/null; \
		for i in $$(seq 40); do \
			curl -s -o /dev/null -m 2 http://127.0.0.1:18131/probe-ready && break; \
			sleep 0.25; \
		done; \
		for escrow in a b c d e f g h; do \
			curl -s -o /dev/null -m 3 \
				"http://127.0.0.1:18131/v4/sessions/esc-$$escrow/chat" || true; \
			curl -s -o /dev/null -m 3 \
				"http://127.0.0.1:18131/sessions/esc-$$escrow/diffs" || true; \
			curl -s -o /dev/null -m 3 \
				"http://127.0.0.1:18131/stats/shards/esc-$$escrow" || true; \
		done; \
		docker logs gonka-hash-probe 2>&1 \
			| awk '/^PICK .*(sessions|stats\/shards)/ { print $$2, $$3 }' | sort; \
		docker rm -f gonka-hash-probe >/dev/null 2>&1 || true; \
	}; \
	probe "127.0.0.2 127.0.0.3 127.0.0.4 127.0.0.5" > "$$tmpdir/forward"; \
	probe "127.0.0.5 127.0.0.4 127.0.0.3 127.0.0.2" > "$$tmpdir/reversed"; \
	probe "127.0.0.4 127.0.0.2 127.0.0.5 127.0.0.3" > "$$tmpdir/shuffled"; \
	[ "$$(wc -l < "$$tmpdir/forward")" -eq 24 ] \
		|| fail "expected three routing decisions per escrow, got: $$(cat "$$tmpdir/forward")"; \
	awk '{ n=split($$1, p, "/"); \
		if (p[2] == "sessions") escrow=p[3]; \
		else if (p[2] == "stats") escrow=p[4]; \
		else escrow=p[4]; \
		if (server[escrow] != "" && server[escrow] != $$2) exit 1; \
		server[escrow]=$$2 \
	}' "$$tmpdir/forward" \
		|| fail "versioned and versionless paths for one escrow chose different hosts"; \
	diff -u "$$tmpdir/forward" "$$tmpdir/reversed" \
		|| fail "reversing the slot order moved sessions: the ring follows the slot, not the host"; \
	diff -u "$$tmpdir/forward" "$$tmpdir/shuffled" \
		|| fail "shuffling the slot order moved sessions: the ring follows the slot, not the host"; \
	echo "test-hash-ring: ok"

# The point of per-version pools is that one host missing one version keeps
# serving the others. That is a property of the running router, not of the text
# of its config, so it is checked by routing real requests through the real
# image at two upstreams that disagree about both coarse and per-version
# readiness. The mismatch also proves that the storage guard counts the backend
# selected for the request instead of the less precise host-level pool.
test-version-routing:
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	net=gonka-version-probe; \
	state=gonka-version-probe-state; \
	cleanup() { \
		docker rm -f gonka-vp-router gonka-vp-a gonka-vp-b gonka-vp-legacy \
			gonka-vp-catalog \
			>/dev/null 2>&1 || true; \
		docker network rm $$net >/dev/null 2>&1 || true; \
		docker volume rm $$state >/dev/null 2>&1 || true; \
		rm -rf "$$tmpdir"; \
	}; \
	trap cleanup EXIT; \
	fail() { echo "test-version-routing: $$1" >&2; exit 1; }; \
	printf '%s\n' \
		'import os, http.server, urllib.parse' \
		'NAME = os.environ["NAME"]' \
		'SERVES = set(os.environ["SERVES"].split())' \
		'GENERIC_READY = os.environ.get("GENERIC_READY", "true") == "true"' \
		'READYZ_SUPPORTED = os.environ.get("READYZ_SUPPORTED", "true") == "true"' \
		'class H(http.server.BaseHTTPRequestHandler):' \
		'    protocol_version = "HTTP/1.1"' \
		'    def _r(self, code, body):' \
		'        b = body.encode()' \
		'        self.send_response(code)' \
		'        self.send_header("Content-Length", str(len(b)))' \
		'        self.end_headers()' \
		'        self.wfile.write(b)' \
		'    def do_GET(self):' \
		'        u = urllib.parse.urlparse(self.path)' \
		'        if u.path == "/readyz":' \
		'            if not READYZ_SUPPORTED:' \
		'                return self._r(404, "legacy")' \
		'            v = urllib.parse.parse_qs(u.query).get("version", [""])[0]' \
		'            ready = v in SERVES if v else GENERIC_READY' \
		'            return self._r(200 if ready else 503, "x")' \
		'        fields = urllib.parse.unquote(u.path).strip("/").split("/")' \
		'        if len(fields) == 2 and fields[1] == "healthz":' \
		'            return self._r(200 if fields[0] in SERVES else 404, "x")' \
		'        if u.path.endswith("/ha-header"):' \
		'            return self._r(200, self.headers.get("Devshard-Ha", ""))' \
		'        if u.path.endswith("/forwarded-headers"):' \
		'            return self._r(200, self.headers.get("X-Forwarded-Proto", "") + "|" + self.headers.get("X-Real-IP", ""))' \
		'        self._r(200, NAME)' \
		'    def log_message(self, *a): pass' \
		'http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()' \
		> "$$tmpdir/upstream.py"; \
	mkdir "$$tmpdir/catalog"; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"}]}' \
		> "$$tmpdir/catalog/versions.json"; \
	printf '%s\n' \
		'import http.server, os, time' \
		'class H(http.server.BaseHTTPRequestHandler):' \
		'    def do_GET(self):' \
		'        if self.path != "/versions": self.send_error(404); return' \
		'        body = open("/data/versions.json", "rb").read()' \
		'        mode = open("/data/mode").read().strip() if os.path.exists("/data/mode") else ""' \
		'        if mode == "slow":' \
		'            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()' \
		'            try:' \
		'                for byte in body + b" " * 256:' \
		'                    self.wfile.write(bytes([byte])); self.wfile.flush(); time.sleep(0.25)' \
		'            except (BrokenPipeError, ConnectionResetError): pass' \
		'            return' \
		'        if mode == "oversized": body += b" " * 2048' \
		'        self.send_response(200)' \
		'        self.send_header("Content-Type", "application/json")' \
		'        if mode != "oversized": self.send_header("Content-Length", str(len(body)))' \
		'        else: self.send_header("Connection", "close"); self.close_connection = True' \
		'        self.end_headers(); self.wfile.write(body)' \
		'    def log_message(self, *a): pass' \
		'http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()' \
		> "$$tmpdir/catalog.py"; \
	docker network create $$net >/dev/null; \
	docker volume create $$state >/dev/null; \
	docker build -q -t gonka-versiond-router-probe \
		-f Dockerfile .. >/dev/null; \
	cache_now=$$(date +%s); \
	docker run --rm --user 0:0 -e CACHE_NOW=$$cache_now \
		-v $$state:/state --entrypoint sh gonka-versiond-router-probe -c \
		'printf "{\"schema\":1,\"fetched_at_unix\":%s,\"versions\":[\"v4\",\"v5\"]}\n" "$$CACHE_NOW" > /state/catalog.json; chown -R haproxy:haproxy /state'; \
	docker run -d --name gonka-vp-catalog --network $$net \
		--network-alias routing-catalog \
		-v "$$tmpdir/catalog:/data:ro" -v "$$tmpdir/catalog.py:/app.py:ro" \
		python:3.12-alpine python /app.py >/dev/null; \
	# Keep one real pool member on the pre-v5 contract. Dynamic admin readiness \
	# must follow the backend's 404 + route-health active check, not proxy /readyz. \
	for spec in "a:v4:false:false" "b:v4 v5 v9 v9+hotfix v10:true:false"; do \
		name=$${spec%%:*}; rest=$${spec#*:}; \
		serves=$${rest%%:*}; rest=$${rest#*:}; \
		generic_ready=$${rest%%:*}; readyz_supported=$${rest##*:}; \
		docker run -d --name gonka-vp-$$name --network $$net --network-alias versiond-pool \
			-e NAME=$$name -e SERVES="$$serves" \
			-e GENERIC_READY=$$generic_ready \
			-e READYZ_SUPPORTED=$$readyz_supported \
			-v "$$tmpdir/upstream.py:/app.py:ro" \
			python:3.12-alpine python /app.py >/dev/null; \
	done; \
	docker run -d --name gonka-vp-legacy --network $$net \
		-e NAME=legacy -e SERVES=v1 -e GENERIC_READY=false \
		-e READYZ_SUPPORTED=false \
		-v "$$tmpdir/upstream.py:/app.py:ro" \
		python:3.12-alpine python /app.py >/dev/null; \
	versions=$$(printf 'v4\t\n\rv5'); \
	legacy_versions=$$(printf 'v1\r\n\tv2'); \
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="$$versions" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS=2 \
		-e VERSIOND_ROUTING_CATALOG_MAX_BYTES=512 \
		-e VERSIOND_ROUTING_CATALOG_RUNTIME_TIMEOUT_SECONDS=1 \
		-e VERSIOND_ROUTING_ACTIVATION_MIN_READY=2 \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=3 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="$$legacy_versions" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 60); do \
		curl -s -o /dev/null -m 2 http://127.0.0.1:18132/v4/sessions/warm/x && break; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router test -s /var/lib/gonka-router/catalog.json \
		|| fail "inner router did not retain the routing catalog cache"; \
	for i in $$(seq 20); do \
		if [ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
			'http://127.0.0.1:18134/readyz?component=catalog')" = 200 ]; then break; fi; \
		sleep 0.5; \
	done; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		'http://127.0.0.1:18134/readyz?component=catalog')" = 200 ] \
		|| fail "catalog convergence is not exposed through the admin readiness endpoint"; \
	docker exec gonka-vp-router wget -q -O - http://127.0.0.1:8405/metrics \
		| grep -Fq 'haproxy_backend_active_servers{proxy="router_catalog_status"} 1' \
		|| fail "catalog convergence is not exposed through HAProxy metrics"; \
	sleep 4; \
	docker exec gonka-vp-router sh -c 'kill -STOP $$(pidof haproxy)' \
		|| fail "could not suspend HAProxy for the Runtime API timeout test"; \
	for i in $$(seq 15); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "runtime-error" and .dynamic_slots_used == 0' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "runtime-error" and .dynamic_slots_used == 0' >/dev/null || { \
			docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state >&2; \
			docker exec gonka-vp-router ps -o pid,stat,comm,args >&2; \
			docker logs --tail 30 gonka-vp-router >&2; \
			fail "a half-open Runtime API socket did not reach its deadline"; \
		}; \
	docker exec gonka-vp-router sh -c 'kill -CONT $$(pidof haproxy)' \
		|| fail "could not resume HAProxy after the Runtime API timeout test"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "ready"' >/dev/null \
		|| fail "catalog reconciliation did not recover after Runtime API resumed"; \
	steady_cache_before=$$(docker exec gonka-vp-router sha256sum \
		/var/lib/gonka-router/catalog.json | awk '{print $$1}'); \
	sleep 2; \
	steady_cache_after=$$(docker exec gonka-vp-router sha256sum \
		/var/lib/gonka-router/catalog.json | awk '{print $$1}'); \
	[ "$$steady_cache_after" = "$$steady_cache_before" ] \
		|| fail "an unchanged catalog rewrote its persistent cache"; \
	for spec in 'v1:200' 'v2:503' 'v4:200' 'v5:200' 'v9:503'; do \
		version=$${spec%%:*}; want=$${spec##*:}; \
		got=$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
			"http://127.0.0.1:18134/readyz?version=$$version"); \
		[ "$$got" = "$$want" ] \
			|| fail "router admin readiness for $$version returned $$got, want $$want"; \
	done; \
	legacy=$$(curl -sS -D "$$tmpdir/legacy.headers" -m 3 \
		http://127.0.0.1:18132/v1/sessions/legacy/chat) \
		|| fail "healthy v1 was hidden by its legacy host's failing generic readiness"; \
	[ "$$legacy" = legacy ] \
		|| fail "legacy v1 reached '$$legacy' instead of its SQLite owner"; \
	tr -d '\r' < "$$tmpdir/legacy.headers" \
		| grep -qi '^x-versiond-backend: versiond_legacy$$' \
		|| fail "legacy diagnostics no longer expose the stable versiond_legacy label"; \
	legacy_v2=$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v2/sessions/legacy/chat); \
	[ "$$legacy_v2" = 503 ] \
		|| fail "legacy host missing v2 answered $$legacy_v2 instead of leaving only v2 down"; \
	ha_header=$$(curl -s -m 3 -H 'Devshard-Ha: forged' \
		http://127.0.0.1:18132/v4/ha-header); \
	[ "$$ha_header" = true ] \
		|| fail "two v4 hosts produced Devshard-Ha='$$ha_header' while the coarse pool had one"; \
	ha_header=$$(curl -s -m 3 -H 'Devshard-Ha: forged' \
		http://127.0.0.1:18132/v5/ha-header); \
	[ -z "$$ha_header" ] \
		|| fail "one v5 host received Devshard-Ha='$$ha_header' from another backend's count"; \
	hosts_for() { \
		for escrow in a b c d e f g h i j k l; do \
			curl -s -m 3 "http://127.0.0.1:18132/$$1/sessions/esc-$$escrow/chat"; \
			echo; \
		done | sort -u | tr '\n' ' '; \
	}; \
	v4=$$(hosts_for v4); v5=$$(hosts_for v5); \
	echo "  v4 -> $$v4"; \
	echo "  v5 -> $$v5"; \
	case "$$v4" in *a*) ;; *) fail "host a serves v4 but got none of its traffic" ;; esac; \
	case "$$v4" in *b*) ;; *) fail "host b serves v4 but got none of its traffic" ;; esac; \
	case "$$v5" in *a*) fail "host a does not serve v5 but received v5 traffic" ;; esac; \
	case "$$v5" in *b*) ;; *) fail "host b serves v5 but got none of its traffic" ;; esac; \
	docker stop gonka-vp-a >/dev/null; \
	for i in $$(seq 20); do \
		if ha_header=$$(curl -fsS -m 3 -H 'Devshard-Ha: forged' \
			http://127.0.0.1:18132/v4/ha-header); then \
			[ -n "$$ha_header" ] || break; \
		fi; \
		sleep 0.5; \
	done; \
	ha_header=$$(curl -fsS -m 3 -H 'Devshard-Ha: forged' \
		http://127.0.0.1:18132/v4/ha-header) \
		|| fail "the surviving backend did not answer the HA-header probe"; \
	[ -z "$$ha_header" ] \
		|| fail "client supplied Devshard-Ha survived as '$$ha_header' with one usable host"; \
	forwarded=$$(curl -fsS -m 3 -H 'X-Forwarded-Proto: forged' \
		-H 'X-Real-IP: forged' http://127.0.0.1:18132/v4/forwarded-headers) \
		|| fail "the forwarded-header probe did not reach an upstream"; \
	case "$$forwarded" in \
		http\|*) ;; \
		*) fail "client supplied forwarded headers survived as '$$forwarded'" ;; \
	esac; \
	case "$$forwarded" in \
		*forged*) fail "client supplied identity survived as '$$forwarded'" ;; \
	esac; \
	undeclared=$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v9/sessions/a/chat); \
	[ "$$undeclared" = 503 ] \
		|| fail "an undeclared version answered $$undeclared; it must be refused, not routed by luck"; \
	curl -s -m 3 http://127.0.0.1:18132/v9/sessions/a/chat \
		| grep -q 'governance routing catalog' \
		|| fail "the refusal does not identify the authoritative catalog"; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/version=v4/sessions/a/chat)" = 503 ] \
		|| fail "a readiness-shaped key bypassed the undeclared-version guard"; \
	health=$$(curl -s -o /dev/null -w '%{http_code}' -m 3 http://127.0.0.1:18132/healthz); \
	[ "$$health" = 200 ] \
		|| fail "the guard broke the router's own /healthz (got $$health)"; \
	for collision in healthz readyz metrics stats sessions; do \
		docker exec gonka-vp-router sh -c \
			'printf "%s\n" "add map /etc/haproxy/versions.map $$1 versiond_pool_v5" | socat - UNIX-CONNECT:/var/run/haproxy/haproxy.sock' \
			sh "$$collision" >/dev/null \
			|| fail "could not inject the $$collision collision into the runtime map"; \
	done; \
	for path in healthz readyz metrics stats/shards/test sessions/test/diffs; do \
		curl -sS -D "$$tmpdir/versionless.headers" -o /dev/null -m 3 \
			"http://127.0.0.1:18132/$$path" \
			|| fail "versionless route /$$path stopped responding"; \
		tr -d '\r' < "$$tmpdir/versionless.headers" \
			| grep -qi '^x-versiond-backend: versiond_ha_pool$$' \
			|| fail "catalog key stole the versionless /$$path route"; \
	done; \
	router_id=$$(docker inspect -f '{{.Id}}' gonka-vp-router); \
	# Dropping one legacy and one static name must not block dynamic additions: \
	# neither name consumes a governance slot or belongs in projected history. \
	printf '%s\n' '{"versions":[{"name":"v2"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "activation-pending" and .dynamic_slots_used == 2' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "activation-pending"' >/dev/null || { \
			docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state >&2; \
			fail "one-host governance versions did not remain candidates"; \
		}; \
	for version in v9 v10; do \
		[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
			"http://127.0.0.1:18134/readyz?version=$$version")" = 503 ] \
			|| fail "$$version was published before the projection reserve"; \
	done; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v11"},{"name":"v12"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "activation-pending" and .dynamic_slots_used == 2 and (.detail | contains("v11"))' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "activation-pending" and .dynamic_slots_used == 2 and (.detail | contains("v11"))' >/dev/null \
		|| fail "withdrawn pending versions did not release their dynamic slots"; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "activation-pending" and .dynamic_slots_used == 2 and (.detail | contains("v10"))' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "activation-pending" and .dynamic_slots_used == 2 and (.detail | contains("v10"))' >/dev/null \
		|| fail "reclaimed dynamic slots could not stage corrected versions"; \
	docker rm -f gonka-vp-a >/dev/null; \
	docker run -d --name gonka-vp-a --network $$net --network-alias versiond-pool \
		-e NAME=a -e SERVES="v4 v9" -e GENERIC_READY=false \
		-e READYZ_SUPPORTED=true -v "$$tmpdir/upstream.py:/app.py:ro" \
		python:3.12-alpine python /app.py >/dev/null; \
	for i in $$(seq 30); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "activation-pending" and (.detail | contains("v10"))' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "activation-pending" and (.detail | contains("v10"))' \
			>/dev/null || fail "v10 did not hold the multi-add projection pending"; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		'http://127.0.0.1:18134/readyz?version=v9')" = 503 ] \
		|| fail "ready v9 was partially published while v10 was still pending"; \
	docker rm -f gonka-vp-a >/dev/null; \
	docker run -d --name gonka-vp-a --network $$net --network-alias versiond-pool \
		-e NAME=a -e SERVES="v4 v9 v10" -e GENERIC_READY=false \
		-e READYZ_SUPPORTED=true -v "$$tmpdir/upstream.py:/app.py:ro" \
		python:3.12-alpine python /app.py >/dev/null; \
	for i in $$(seq 30); do \
		if curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9' >/dev/null \
			&& curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v10' >/dev/null; then break; fi; \
		sleep 0.5; \
	done; \
	for version in v9 v10; do \
		curl -fsS -m 3 "http://127.0.0.1:18134/readyz?version=$$version" >/dev/null \
			|| fail "catalog projection did not admit $$version"; \
	done; \
	docker exec gonka-vp-router sh -c \
		'printf "%s\n" "show map" | socat - UNIX-CONNECT:/var/run/haproxy/haproxy.sock' \
		| grep -E 'versions\.map.*curr_ver=0 next_ver=0' >/dev/null \
		|| fail "catalog publication left prepared HAProxy map generations"; \
	docker rm -f gonka-vp-a >/dev/null; \
	docker run -d --name gonka-vp-a --network $$net --network-alias versiond-pool \
		-e NAME=a -e SERVES=v4 -e GENERIC_READY=false \
		-e READYZ_SUPPORTED=false -v "$$tmpdir/upstream.py:/app.py:ro" \
		python:3.12-alpine python /app.py >/dev/null; \
	dynamic_host=; \
	for i in $$(seq 20); do \
		dynamic_host=$$(curl -fsS -m 3 \
			http://127.0.0.1:18132/v9/sessions/dynamic/chat 2>/dev/null || true); \
		[ "$$dynamic_host" = b ] && break; \
		sleep 0.5; \
	done; \
	[ "$$dynamic_host" = b ] \
		|| fail "dynamically learned v9 reached '$$dynamic_host' instead of its ready host"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v9 \
		|| fail "runtime catalog does not report dynamically learned v9"; \
	[ "$$(docker inspect -f '{{.Id}}' gonka-vp-router)" = "$$router_id" ] \
		|| fail "learning v9 replaced the router container"; \
	stable_host=$$(curl -fsS -m 3 \
		http://127.0.0.1:18132/v4/sessions/still-live/chat) \
		|| fail "the existing v4 route stopped responding after learning v9"; \
	case "$$stable_host" in \
		a|b) ;; \
		*) fail "learning v9 sent the existing v4 route to '$$stable_host'" ;; \
	esac; \
	bounded_cache_before=$$(docker exec gonka-vp-router sha256sum \
		/var/lib/gonka-router/catalog.json | awk '{print $$1}'); \
	printf '%s\n' slow > "$$tmpdir/catalog/mode.next"; \
	mv "$$tmpdir/catalog/mode.next" "$$tmpdir/catalog/mode"; \
	for i in $$(seq 16); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "fetch-error"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "fetch-error"' >/dev/null \
		|| fail "a continuously trickling catalog response bypassed the total deadline"; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		'http://127.0.0.1:18134/readyz?component=catalog')" = 503 ] \
		|| fail "catalog fetch failure remained green on the admin readiness endpoint"; \
	docker exec gonka-vp-router wget -q -O - http://127.0.0.1:8405/metrics \
		| grep -Fq 'haproxy_backend_active_servers{proxy="router_catalog_status"} 0' \
		|| fail "catalog fetch failure remained green in HAProxy metrics"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/slow-source/chat)" = b ] \
		|| fail "a slow catalog response removed the accepted v9 route"; \
	slow_observed_at=$$(docker exec gonka-vp-router \
		/usr/local/lib/router-runtime/catalog-status --state | jq -er .observed_at_unix); \
	printf '%s\n' oversized > "$$tmpdir/catalog/mode.next"; \
	mv "$$tmpdir/catalog/mode.next" "$$tmpdir/catalog/mode"; \
	for i in $$(seq 16); do \
		oversized_status=$$(docker exec gonka-vp-router \
			/usr/local/lib/router-runtime/catalog-status --state 2>/dev/null || true); \
		if printf '%s\n' "$$oversized_status" | jq -e \
			--argjson previous "$$slow_observed_at" \
			'.state == "fetch-error" and .observed_at_unix > $$previous' \
			>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	printf '%s\n' "$$oversized_status" | jq -e \
		--argjson previous "$$slow_observed_at" \
		'.state == "fetch-error" and .observed_at_unix > $$previous' >/dev/null \
		|| fail "an oversized catalog response bypassed the response-body limit"; \
	bounded_cache_after=$$(docker exec gonka-vp-router sha256sum \
		/var/lib/gonka-router/catalog.json | awk '{print $$1}'); \
	[ "$$bounded_cache_after" = "$$bounded_cache_before" ] \
		|| fail "a bounded catalog fetch replaced the accepted snapshot"; \
	rm -f "$$tmpdir/catalog/mode"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "ready"' >/dev/null \
		|| fail "catalog reconciliation did not recover after bounded fetch failures"; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		'http://127.0.0.1:18134/readyz?component=catalog')" = 200 ] \
		|| fail "catalog readiness did not recover with reconciliation"; \
	docker exec gonka-vp-router test -s /var/lib/gonka-router/catalog.json \
		|| fail "inner router did not persist its learned catalog"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-cache read \
		/var/lib/gonka-router/catalog.json 86400 > "$$tmpdir/projected-cache.names"; \
	printf '%s\n' v10 v9 | LC_ALL=C sort > "$$tmpdir/want-projected-cache.names"; \
	diff -u "$$tmpdir/want-projected-cache.names" "$$tmpdir/projected-cache.names" \
		|| fail "persistent catalog history contains static or legacy names"; \
	runtime_router_id=$$(docker inspect -f '{{.Id}}' gonka-vp-router); \
	docker stop gonka-vp-catalog >/dev/null; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "fetch-error"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	reconciler_observed_at=$$(docker exec gonka-vp-router \
		/usr/local/lib/router-runtime/catalog-status --state | jq -er .observed_at_unix); \
	docker exec gonka-vp-router sh -c \
		'printf "%s\n" "{not-json" > /var/lib/gonka-router/catalog.json'; \
	docker exec gonka-vp-router sh -c \
		'pid=$$(ps -o pid,comm | awk "\$$2 ~ /^catalog-recon/ { print \$$1; exit }"); [ -n "$$pid" ]; kill "$$pid"'; \
	for i in $$(seq 30); do \
		runtime_recovery_status=$$(docker exec gonka-vp-router \
			/usr/local/lib/router-runtime/catalog-status --state 2>/dev/null || true); \
		if printf '%s\n' "$$runtime_recovery_status" | jq -e \
			--argjson previous "$$reconciler_observed_at" \
			'.state == "fetch-error" and .observed_at_unix > $$previous' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	printf '%s\n' "$$runtime_recovery_status" | jq -e \
		--argjson previous "$$reconciler_observed_at" \
		'.state == "fetch-error" and .observed_at_unix > $$previous' >/dev/null \
		|| fail "reconciler did not restart after its cache became corrupt"; \
	[ "$$(docker inspect -f '{{.Id}}' gonka-vp-router)" = "$$runtime_router_id" ] \
		|| fail "the corrupt-cache scenario replaced the HAProxy container"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/live-recovery/chat)" = b ] \
		|| fail "reconciler restart with a corrupt cache removed a live route"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-cache read \
		/var/lib/gonka-router/catalog.json 86400 > "$$tmpdir/runtime-recovered.names"; \
	diff -u "$$tmpdir/want-projected-cache.names" "$$tmpdir/runtime-recovered.names" \
		|| fail "live accepted routes did not repair the corrupt cache"; \
	docker start gonka-vp-catalog >/dev/null; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	cache_before=$$(docker exec gonka-vp-router sha256sum \
		/var/lib/gonka-router/catalog.json | awk '{print $$1}'); \
	printf '%s\n' '{"versions":[]}' > "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "projection-error"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "projection-error"' >/dev/null \
		|| fail "an empty catalog bypassed the last-known-good removal check"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/empty-catalog/chat)" = b ] \
		|| fail "an empty catalog removed the admitted v9 route"; \
	cache_after=$$(docker exec gonka-vp-router sha256sum \
		/var/lib/gonka-router/catalog.json | awk '{print $$1}'); \
	[ "$$cache_after" = "$$cache_before" ] \
		|| fail "an empty catalog replaced the persistent last-known-good snapshot"; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "ready"' >/dev/null \
		|| fail "catalog reconciliation did not recover after the empty snapshot"; \
	docker stop gonka-vp-catalog >/dev/null; \
	docker rm -f gonka-vp-router >/dev/null; \
	stale_accepted_at=$$(($$(date +%s) - 90000)); \
	docker run --rm --user 0:0 -e STALE_ACCEPTED_AT=$$stale_accepted_at \
		-v $$state:/state --entrypoint sh gonka-versiond-router-probe -c \
		'jq ".fetched_at_unix = (env.STALE_ACCEPTED_AT | tonumber)" /state/catalog.json > /state/catalog.next && chmod 600 /state/catalog.next && mv /state/catalog.next /state/catalog.json && chown haproxy:haproxy /state/catalog.json' \
		|| fail "could not age the accepted catalog cache"; \
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTING_ACTIVATION_MIN_READY=2 \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=3 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="v1 v2" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 30); do \
		if curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9' >/dev/null; then break; fi; \
		sleep 0.5; \
	done; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/cache/chat)" = b ] \
		|| fail "inner router discarded a stale accepted route when restarted without dapi"; \
	printf '%s\n' 'del map /etc/haproxy/versions.map v9' \
		| docker exec -i gonka-vp-router socat - \
			UNIX-CONNECT:/var/run/haproxy/reconciler.sock; \
	repaired_host=; \
	for i in $$(seq 20); do \
		repaired_host=$$(curl -fsS -m 3 \
			http://127.0.0.1:18132/v9/sessions/cache-repair/chat 2>/dev/null || true); \
		[ "$$repaired_host" = b ] && break; \
		sleep 0.5; \
	done; \
	[ "$$repaired_host" = b ] \
		|| fail "accepted catalog did not repair the runtime map while dapi was unavailable"; \
	docker start gonka-vp-catalog >/dev/null; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"},{"name":"v11"},{"name":"v11"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	sleep 2; \
	if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v11; then \
		fail "an invalid duplicate catalog snapshot changed the route map"; \
	fi; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/after-invalid/chat)" = b ] \
		|| fail "an invalid catalog snapshot removed the last valid route view"; \
	printf '%s\n' '{"versions":"not-an-array"}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	sleep 2; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "fetch-error" and .dynamic_slots_used == 2' >/dev/null \
		|| fail "catalog fetch errors did not retain the last known slot usage"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/after-malformed/chat)" = b ] \
		|| fail "a malformed catalog replaced the last accepted route view"; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 10); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready" and .dynamic_slots_used == 2' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "ready" and .dynamic_slots_used == 2' >/dev/null \
		|| fail "catalog reconciliation did not recover before the framing test"; \
	docker rm -f gonka-vp-a >/dev/null; \
	docker run -d --name gonka-vp-a --network $$net --network-alias versiond-pool \
		-e NAME=a -e SERVES="v4 v9+hotfix" -e GENERIC_READY=false \
		-e READYZ_SUPPORTED=true -v "$$tmpdir/upstream.py:/app.py:ro" \
		python:3.12-alpine python /app.py >/dev/null; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"},{"name":"v:6"},{"name":"v9+hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 30); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "contract-error" and .dynamic_slots_used == 3' \
				>/dev/null 2>&1 \
			&& curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%2Bhotfix' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "contract-error" and .dynamic_slots_used == 3' >/dev/null \
		|| fail "one incompatible name blocked compatible catalog additions"; \
	if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -Fqx 'v:6'; then \
		fail "an incompatible governance name reached the routing map"; \
	fi; \
	contract_host=$$(curl -fsS -m 3 \
		'http://127.0.0.1:18132/v9+hotfix/sessions/contract/chat'); \
	case "$$contract_host" in \
		a|b) ;; \
		*) fail "a compatible addition beside an incompatible name reached '$$contract_host'" ;; \
	esac; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"},{"name":"v9+hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready" and .dynamic_slots_used == 3' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	framing_cache_before=$$(docker exec gonka-vp-router sha256sum \
		/var/lib/gonka-router/catalog.json | awk '{print $$1}'); \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v9"},{"name":"v10"},{"name":"v9+hotfix"},{"name":"v11\nv5"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 10); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
				| jq -e '.state == "contract-error" and .dynamic_slots_used == 3' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "contract-error" and .dynamic_slots_used == 3' >/dev/null \
		|| fail "an embedded newline split one governance name into multiple routes"; \
	if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v11; then \
		fail "an embedded newline published a fabricated catalog route"; \
	fi; \
	framing_cache_after=$$(docker exec gonka-vp-router sha256sum \
		/var/lib/gonka-router/catalog.json | awk '{print $$1}'); \
	[ "$$framing_cache_after" = "$$framing_cache_before" ] \
		|| fail "an embedded newline replaced the persistent catalog snapshot"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/after-framing-error/chat)" = b ] \
		|| fail "an embedded newline replaced the last accepted route view"; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v9+hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "withdrawal-pending" and .dynamic_slots_used == 3' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "withdrawal-pending" and .dynamic_slots_used == 3' >/dev/null \
		|| fail "an unversioned withdrawal was not held for maintenance"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v10 \
		|| fail "an unversioned snapshot removed an accepted route"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-cache read \
		/var/lib/gonka-router/catalog.json 86400 > "$$tmpdir/after-removal.names"; \
	grep -Fqx v9 "$$tmpdir/after-removal.names" \
		|| fail "holding one withdrawal removed a surviving route from the durable cache"; \
	grep -Fqx v10 "$$tmpdir/after-removal.names" \
		|| fail "an unversioned withdrawal removed the durable route"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v10/sessions/held/chat)" = b ] \
		|| fail "an unversioned withdrawal stopped an otherwise healthy route"; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"},{"name":"v9+hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready" and .dynamic_slots_used == 3' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	# Maintenance mode is explicit: only this replacement may retire v10. \
	docker rm -f gonka-vp-router >/dev/null; \
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS=2 \
		-e VERSIOND_ROUTING_CATALOG_MAX_BYTES=512 \
		-e VERSIOND_ROUTING_CATALOG_RUNTIME_TIMEOUT_SECONDS=1 \
		-e VERSIOND_ROUTING_ACTIVATION_MIN_READY=2 \
		-e VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=true \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=3 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="v1 v2" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready" and .dynamic_slots_used == 3' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	retired_slot=$$(docker exec gonka-vp-router sh -c \
		'printf "%s\n" "show map /etc/haproxy/versions.map" | socat - UNIX-CONNECT:/var/run/haproxy/reconciler.sock' \
		| awk '$$2 == "v10" { print $$3; exit }'); \
	[ -n "$$retired_slot" ] || fail "could not identify v10's maintenance slot"; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v9+hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "ready" and .dynamic_slots_used == 2' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "ready" and .dynamic_slots_used == 2' >/dev/null \
		|| fail "maintenance mode did not retire the withdrawn route"; \
	if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v10; then \
		fail "maintenance mode left the withdrawn route published"; \
	fi; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v10/sessions/removed/chat)" = 503 ] \
		|| fail "a maintenance-removed governance route still accepted traffic"; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"},{"name":"v9+hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 30); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "activation-pending" and (.detail | contains("v10"))' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	replacement_slot=$$(docker exec gonka-vp-router sh -c \
		'printf "%s\n" "show map /etc/haproxy/haproxy.cfg.version-slots.map" | socat - UNIX-CONNECT:/var/run/haproxy/reconciler.sock' \
		| awk '$$3 == "v10" { print $$2; exit }'); \
	[ "$$replacement_slot" = "$$retired_slot" ] \
		|| fail "the removal scenario did not reuse v10's dynamic slot"; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v10/sessions/readded-pending/chat)" = 503 ] \
		|| fail "a re-added route bypassed the activation reserve"; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		'http://127.0.0.1:18134/readyz?version=v10')" = 503 ] \
		|| fail "a cached readiness rule bypassed the re-add activation reserve"; \
	docker rm -f gonka-vp-a >/dev/null; \
	docker run -d --name gonka-vp-a --network $$net --network-alias versiond-pool \
		-e NAME=a -e SERVES="v4 v9+hotfix v10" -e GENERIC_READY=false \
		-e READYZ_SUPPORTED=true -v "$$tmpdir/upstream.py:/app.py:ro" \
		python:3.12-alpine python /app.py >/dev/null; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"},{"name":"v9+hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 30); do \
		if curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%2Bhotfix' >/dev/null \
			&& curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v10' >/dev/null; then break; fi; \
		sleep 0.5; \
	done; \
	curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%2Bhotfix' >/dev/null \
		|| fail "encoded admin readiness did not follow the dynamic governance map"; \
	curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v10' >/dev/null \
		|| fail "a re-added route did not publish after regaining its reserve"; \
	dynamic_host=$$(curl -fsS -m 3 \
		'http://127.0.0.1:18132/v9+hotfix/sessions/escaped/chat'); \
	case "$$dynamic_host" in \
		a|b) ;; \
		*) fail "a routable governance name requiring Runtime API escaping reached '$$dynamic_host'" ;; \
	esac; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -Fqx 'v9+hotfix' \
		|| fail "runtime catalog lost the governance version name"; \
	if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -q '^version='; then \
		fail "runtime catalog diagnostics exposed readiness projection keys as versions"; \
	fi; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"},{"name":"v9+hotfix"},{"name":"v11"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 30); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "capacity-exhausted" and .dynamic_slots_used == 3' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v11; then \
		fail "catalog capacity exhaustion silently published v11"; \
	fi; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "capacity-exhausted" and .dynamic_slots_used == 3' \
			>/dev/null || { \
			docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state >&2; \
			fail "catalog capacity exhaustion is not exposed as persistent degraded state"; \
		}; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v11/sessions/capacity/chat)" = 503 ] \
		|| fail "capacity-exhausted v11 was routed without an assigned backend"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/persisted/chat)" = b ] \
		|| fail "a later catalog snapshot removed an already admitted route"; \
	docker exec gonka-vp-router sh -c \
		'/usr/local/lib/router-runtime/catalog-cache read /var/lib/gonka-router/catalog.json 86400 > /tmp/accepted.names && /usr/local/lib/router-runtime/catalog-cache write /tmp/accepted.names /var/lib/gonka-router/catalog.json && rm -f /tmp/accepted.names' \
		|| fail "could not establish a fresh accepted cache for the pending-addition test"; \
	pending_cache_before=$$(docker exec gonka-vp-router \
		jq -er .fetched_at_unix /var/lib/gonka-router/catalog.json); \
	docker rm -f gonka-vp-router >/dev/null; \
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS=6 \
		-e VERSIOND_ROUTING_ACTIVATION_MIN_READY=2 \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=3 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="v1 v2" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 20); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			2>/dev/null \
			| jq -e '.state == "capacity-exhausted"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	for i in $$(seq 16); do \
		pending_cache_after=$$(docker exec gonka-vp-router \
			jq -er .fetched_at_unix /var/lib/gonka-router/catalog.json); \
		now=$$(date +%s); \
		if [ $$((now - pending_cache_before)) -gt 6 ] \
			&& [ "$$pending_cache_after" -gt "$$pending_cache_before" ]; then break; fi; \
		sleep 0.5; \
	done; \
	[ $$((now - pending_cache_before)) -gt 6 ] \
		&& [ "$$pending_cache_after" -gt "$$pending_cache_before" ] \
		|| fail "a pending addition allowed the accepted catalog cache to expire"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-cache read \
		/var/lib/gonka-router/catalog.json 6 > "$$tmpdir/pending-cache.names" \
		|| fail "the accepted cache was stale while the source catalog remained fresh"; \
	grep -Fqx v9 "$$tmpdir/pending-cache.names" \
		|| fail "refreshing the accepted cache lost an admitted route"; \
	if grep -Fqx v11 "$$tmpdir/pending-cache.names"; then \
		fail "refreshing the accepted cache persisted a capacity-blocked route"; \
	fi; \
	docker stop gonka-vp-catalog >/dev/null; \
	docker rm -f gonka-vp-router >/dev/null; \
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS=6 \
		-e VERSIOND_ROUTING_ACTIVATION_MIN_READY=2 \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=3 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="v1 v2" \
		gonka-versiond-router-probe >/dev/null; \
	restored_host=; \
	for i in $$(seq 30); do \
		restored_host=$$(curl -fsS -m 3 \
			http://127.0.0.1:18132/v9/sessions/pending-cache/chat 2>/dev/null || true); \
		[ "$$restored_host" = b ] && break; \
		sleep 0.5; \
	done; \
	[ "$$restored_host" = b ] \
		|| fail "router replacement lost an accepted route behind a pending addition"; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v11/sessions/pending-cache/chat)" = 503 ] \
		|| fail "router replacement restored a capacity-blocked route"; \
	docker start gonka-vp-catalog >/dev/null; \
	docker rm -f gonka-vp-router >/dev/null; \
	printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9+hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	docker run --rm --user 0:0 -v $$state:/state \
		--entrypoint sh gonka-versiond-router-probe -c \
		'printf "%s\n" "{not-json" > /state/catalog.json; chown haproxy:haproxy /state/catalog.json'; \
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTING_ACTIVATION_MIN_READY=2 \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=3 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="v1 v2" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 30); do \
		if curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%2Bhotfix' \
			>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%2Bhotfix' >/dev/null \
		|| fail "a valid governance catalog did not recover an invalid persistent cache"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-cache read \
		/var/lib/gonka-router/catalog.json 86400 | grep -Fqx 'v9+hotfix' \
		|| fail "the recovered catalog did not replace the invalid persistent cache"; \
	docker rm -f gonka-vp-router >/dev/null; \
	stale_at=$$(($$(date +%s) - 60)); \
	docker run --rm --user 0:0 -e STALE_AT=$$stale_at -v $$state:/state \
		--entrypoint sh gonka-versiond-router-probe -c \
		'printf "{\"schema\":1,\"fetched_at_unix\":%s,\"versions\":[\"v1\",\"v2\",\"v4\",\"v5\",\"v-obsolete\"]}\n" "$$STALE_AT" > /state/catalog.json; chown haproxy:haproxy /state/catalog.json'; \
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTING_ACTIVATION_MIN_READY=2 \
		-e VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=true \
		-e VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS=1 \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=3 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="v1 v2" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 30); do \
		if curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%2Bhotfix' \
			>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%2Bhotfix' >/dev/null \
		|| fail "a stale persistent cache blocked the fresh governance catalog"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "ready"' >/dev/null \
		|| fail "stale cache recovery left catalog reconciliation degraded"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-cache read \
		/var/lib/gonka-router/catalog.json 86400 > "$$tmpdir/recovered.names"; \
	grep -Fqx 'v9+hotfix' "$$tmpdir/recovered.names" \
		|| fail "the fresh catalog did not replace the stale persistent cache"; \
	if grep -Fqx 'v-obsolete' "$$tmpdir/recovered.names"; then \
		fail "the stale catalog remained accepted history after recovery"; \
	fi; \
	echo "test-version-routing: ok"

test-render: test-local-compose test-join-compose test-catalog-cache test-hash-ring test-version-routing
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
	HAPROXY_DNS_RESOLVER=10.96.0.10:53 \
	VERSIOND_VERSIONS='v4,v5' \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/mixed.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/mixed.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/mixed.versions.map" \
	VERSIOND_ROUTER_READY_RULES="$$tmpdir/mixed.ready.rules" \
	./entrypoint.sh; \
	grep -q '^[[:space:]]*server-template versiond 64 versiond-pool:8080' "$$tmpdir/mixed.cfg" \
		|| fail "HA pool is not a DNS-resolved server-template"; \
	grep -q '^[[:space:]]*hash-type consistent' "$$tmpdir/mixed.cfg" \
		|| fail "HA pool lost consistent hashing"; \
	grep -q 'set-var(txn.hashkey).*field(3,/).*sessions' "$$tmpdir/mixed.cfg" \
		|| fail "versionless session routes do not reuse the escrow routing key"; \
	grep -q '^[[:space:]]*nameserver dns 10.96.0.10:53$$' "$$tmpdir/mixed.cfg" \
		|| fail "the configured DNS resolver was not rendered"; \
	grep -q '^[[:space:]]*server-template versiond .*hash-key addr' "$$tmpdir/mixed.cfg" \
		|| fail "pool servers must hash by address; by slot id a router restart re-homes every session"; \
	grep -q '^[[:space:]]*http-check send meth GET uri /readyz' "$$tmpdir/mixed.cfg" \
		|| fail "active health checks are not configured"; \
	grep -q '^[[:space:]]*bind :8404' "$$tmpdir/mixed.cfg" \
		|| fail "internal router readiness is not on its dedicated admin port"; \
	grep -q 'query -m str version=v5.*nbsrv(versiond_pool_v5) gt 0' "$$tmpdir/mixed.cfg" \
		|| fail "router readiness does not use the selected version backend"; \
	grep -q '^[[:space:]]*http-check expect status 200,404' "$$tmpdir/mixed.cfg" \
		|| fail "pre-v5 readiness capability fallback is not configured"; \
	grep -q '^[[:space:]]*http-check send meth GET uri /healthz' "$$tmpdir/mixed.cfg" \
		|| fail "coarse readiness does not verify the legacy health contract"; \
	grep -Eq '^[[:space:]]*http-request set-header Devshard-Ha true[[:space:]]*$$' "$$tmpdir/mixed.cfg" \
		|| fail "GONKA_HA did not enable the Devshard-Ha header"; \
	grep -q '^[[:space:]]*server-template versiond 1 versiond:8080' "$$tmpdir/mixed.cfg" \
		|| fail "legacy backends do not point at the single VERSIOND_LEGACY_HOST"; \
	grep -q '^[[:space:]]*retry-on conn-failure empty-response 502' "$$tmpdir/mixed.cfg" \
		|| fail "failover no longer excludes 503"; \
	awk '/^backend versiond_ha_pool$$/{inside=1; next} /^backend /{inside=0} \
		inside && /retry-on conn-failure empty-response 502 404/{found=1} END{exit !found}' \
		"$$tmpdir/mixed.cfg" \
		|| fail "versionless GET requests cannot retry a route-local 404"; \
	awk '/^backend versiond_pool_v5$$/{inside=1; next} /^backend /{inside=0} \
		inside && /retry-on conn-failure empty-response 502$$/{found=1} \
		inside && /retry-on .* 404/{bad=1} END{exit !found || bad}' "$$tmpdir/mixed.cfg" \
		|| fail "explicit version backends retry authoritative 404 responses"; \
	grep -q '^[[:space:]]*http-request disable-l7-retry if !METH_GET' "$$tmpdir/mixed.cfg" \
		|| fail "non-idempotent requests could be replayed"; \
	grep -q '^[[:space:]]*option redispatch[[:space:]]*$$' "$$tmpdir/mixed.cfg" \
		|| fail "connection retries no longer stay with the hash owner first"; \
	if grep -q '^[[:space:]]*option redispatch 1' "$$tmpdir/mixed.cfg"; then \
		fail "the first connection retry immediately abandons the hash owner"; \
	fi; \
	grep -q '^[[:space:]]*bind 127.0.0.1:8405' "$$tmpdir/mixed.cfg" \
		|| fail "metrics are not bound to loopback"; \
	grep -q '^[[:space:]]*option[[:space:]]*forwardfor' "$$tmpdir/mixed.cfg" \
		|| fail "X-Forwarded-For is no longer appended; the client audit trail dies at this hop"; \
	grep -Eq '^[[:space:]]*http-request set-header X-Real-IP %\[src\][[:space:]]*$$' "$$tmpdir/mixed.cfg" \
		|| fail "X-Real-IP is not unconditionally replaced"; \
	grep -Eq '^[[:space:]]*http-request set-header X-Forwarded-Proto %\[ssl_fc,iif\(https,http\)\][[:space:]]*$$' "$$tmpdir/mixed.cfg" \
		|| fail "X-Forwarded-Proto is not unconditionally replaced"; \
	grep -q '^[[:space:]]*http-request deny deny_status 413 if { req.hdr_val(content-length) gt 10485760 }' "$$tmpdir/mixed.cfg" \
		|| fail "the advertised request body cap is gone"; \
	grep -Eq '^[[:space:]]*accepted_payload_size 8192([[:space:]]|$$)' "$$tmpdir/mixed.cfg" \
		|| fail "DNS replies are still capped below the configured pool capacity"; \
	grep -Eq '^[[:space:]]*timeout check 3s([[:space:]]|$$)' "$$tmpdir/mixed.cfg" \
		|| fail "no timeout check directive: inter would double as the whole check budget and a slow answer would read as down"; \
	grep -q '^[[:space:]]*http-check send meth GET uri /readyz?version=v4' "$$tmpdir/mixed.cfg" \
		|| fail "declared versions must be health-checked individually"; \
	grep -q '^[[:space:]]*http-check send meth GET uri /v4/healthz' "$$tmpdir/mixed.cfg" \
		|| fail "declared versions lack the pre-v5 route-health fallback"; \
	grep -q '^backend versiond_pool_v5$$' "$$tmpdir/mixed.cfg" \
		|| fail "no backend for declared version v5"; \
	[ "$$(grep -c '^backend versiond_dynamic_' "$$tmpdir/mixed.cfg")" -eq 32 ] \
		|| fail "the default dynamic governance capacity is not pre-rendered"; \
	[ "$$(grep -c '^versiond_dynamic_.* __unassigned__$$' \
		"$$tmpdir/mixed.cfg.version-slots.map")" -eq 32 ] \
		|| fail "dynamic backend slots are not initialized as unassigned"; \
	awk '/^backend versiond_dynamic_1$$/{inside=1; next} /^backend /{inside=0} \
		inside && /server-template .* disabled$$/{found=1} END{exit !found}' \
		"$$tmpdir/mixed.cfg" \
		|| fail "unused dynamic slots are performing health checks"; \
	grep -q 'stats socket /var/run/haproxy/reconciler.sock mode 600 level admin' \
		"$$tmpdir/mixed.cfg" \
		|| fail "the catalog reconciler does not have a dedicated admin socket"; \
	grep -q 'stats socket /var/run/haproxy/haproxy.sock mode 660 level user' \
		"$$tmpdir/mixed.cfg" \
		|| fail "the diagnostic Runtime API exceeds HAProxy's minimum command level"; \
	for version in v1 v2 v3; do \
		backend=versiond_legacy_$$version; \
		grep -q "^backend $$backend$$" "$$tmpdir/mixed.cfg" \
			|| fail "no isolated legacy backend for $$version"; \
		awk -v b="backend $$backend" -v uri="/readyz?version=$$version" \
			'$$0 == b {inside=1; next} /^backend /{inside=0} \
			 inside && /http-check send/ && index($$0, uri) {found=1} \
			 END {exit !found}' "$$tmpdir/mixed.cfg" \
			|| fail "$$backend does not check $$version independently"; \
		awk -v b="backend $$backend" -v uri="/$$version/healthz" \
			'$$0 == b {inside=1; next} /^backend /{inside=0} \
			 inside && /http-check send/ && index($$0, uri) {found=1} \
			 END {exit !found}' "$$tmpdir/mixed.cfg" \
			|| fail "$$backend lacks the pre-v5 route-health fallback"; \
		awk -v b="backend $$backend" \
			'$$0 == b {inside=1; next} /^backend /{inside=0} \
			 inside && /http-request del-header Devshard-Ha/ {strips=1} \
			 inside && /X-Versiond-Backend versiond_legacy/ {label=1} \
			 END {exit !(strips && label)}' "$$tmpdir/mixed.cfg" \
			|| fail "$$backend lost the single-owner header contract"; \
	done; \
	if grep -q '^backend versiond_legacy$$' "$$tmpdir/mixed.cfg"; then \
		fail "one generic legacy backend still couples every pinned version to /readyz"; \
	fi; \
	grep -q '^[[:space:]]*use_backend %\[var(txn.ver),map_str' "$$tmpdir/mixed.cfg" \
		|| fail "requests are not routed to their version's pool"; \
	awk '/use_backend versiond_ha_pool if versionless_request/ { protected = NR } \
		/use_backend %\[var\(txn.ver\),map_str/ && first_map == 0 { first_map = NR } \
		END { exit !(protected > 0 && first_map > protected) }' \
		"$$tmpdir/mixed.cfg" \
		|| fail "version maps can take precedence over versionless routes"; \
	printf '%s\n' \
		'v4 versiond_pool_v4' \
		'v5 versiond_pool_v5' \
		> "$$tmpdir/want.versions.map"; \
	diff -u "$$tmpdir/want.versions.map" "$$tmpdir/mixed.versions.map" \
		|| fail "declared versions rendered incorrectly"; \
	if grep -q '^version=' "$$tmpdir/mixed.versions.map"; then \
		fail "readiness-shaped keys leaked into the data-plane routing map"; \
	fi; \
	for backend in versiond_ha_pool versiond_pool_v4 versiond_pool_v5 \
		versiond_legacy_v1 versiond_legacy_v2 versiond_legacy_v3; do \
		count=$$(awk -v b="backend $$backend" '$$0 == b {inside=1; next} /^backend /{inside=0} \
			inside && $$0 !~ /^[[:space:]]*#/ && \
			/hash-key addr|http-reuse safe|disable-l7-retry|retry-on|hash-type consistent/ {n++} \
			END {print n + 0}' "$$tmpdir/mixed.cfg"); \
		[ "$$count" -eq 5 ] \
			|| fail "$$backend has $$count of 5 policy lines; the pools have drifted apart"; \
	done; \
	printf 'v1 versiond_legacy_v1\nv2 versiond_legacy_v2\nv3 versiond_legacy_v3\n' \
		> "$$tmpdir/want.map"; \
	diff -u "$$tmpdir/want.map" "$$tmpdir/mixed.map" \
		|| fail "pinned versions rendered incorrectly"; \
	HAPROXY_BIN=true \
	VERSIOND_ROUTER_RENDER_ONLY=1 \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/allha.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/allha.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/allha.versions.map" \
	./entrypoint.sh; \
	[ ! -s "$$tmpdir/allha.versions.map" ] \
		|| fail "no versions were declared but per-version pools were rendered"; \
	[ ! -s "$$tmpdir/allha.map" ] \
		|| fail "no pinned versions were requested but the map is not empty"; \
	if grep -q '^backend versiond_legacy_' "$$tmpdir/allha.cfg"; then \
		fail "legacy backends were rendered without pinned versions"; \
	fi; \
	sed -n '/^frontend versiond_in$$/,/^backend /p' "$$tmpdir/allha.cfg" \
		| grep -q '^[[:space:]]*http-request del-header Devshard-Ha' \
		|| fail "the frontend must strip a client-supplied Devshard-Ha"; \
	grep -Fq 'http-request set-header Devshard-Ha true if { nbsrv(versiond_ha_pool) gt 1 }' "$$tmpdir/allha.cfg" \
		|| fail "unset GONKA_HA lost the runtime multi-host storage guard"; \
	grep -Fq 'string "catalog disabled\n" if { path /readyz } { url_param(component) -m str catalog }' \
		"$$tmpdir/allha.cfg" \
		|| fail "static routing mode does not expose catalog readiness as not applicable"; \
	grep -Eq '^    server catalog 127\.0\.0\.1:1[[:space:]]*$$' "$$tmpdir/allha.cfg" \
		|| fail "static routing mode leaves the catalog status metric degraded"; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		GONKA_HA=true \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
		VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bad.versions.map" \
		./entrypoint.sh 2>/dev/null; then \
		fail "an HA deployment with no declared versions was accepted"; \
	fi; \
	HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		GONKA_HA=true VERSIOND_ROUTING_CATALOG_URL=http://catalog:8080/versions \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/catalog-only.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/catalog-only.map" \
		VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/catalog-only.versions.map" \
		./entrypoint.sh \
		|| fail "the governance catalog cannot bootstrap an HA router without a static version list"; \
	grep -q 'governance routing catalog' "$$tmpdir/catalog-only.cfg" \
		|| fail "catalog mode does not fail closed for names absent from governance"; \
	grep -q '^    server catalog 127\.0\.0\.1:1 disabled$$' "$$tmpdir/catalog-only.cfg" \
		|| fail "catalog mode starts with a false-green status metric"; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTING_CATALOG_URL=ftp://catalog/versions \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad-catalog.cfg" \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "an unsupported catalog URL scheme was accepted"; \
	fi; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTING_CATALOG_POLL_SECONDS=typo \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad-catalog.cfg" \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "an invalid catalog poll interval was accepted"; \
	fi; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTING_CATALOG_RUNTIME_TIMEOUT_SECONDS=0 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad-catalog.cfg" \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "a zero catalog Runtime API timeout was accepted"; \
	fi; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTING_CATALOG_MAX_BYTES=0 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad-catalog.cfg" \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "a zero catalog response-body limit was accepted"; \
	fi; \
	for off in 0 false NO; do \
		if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
			GONKA_HA=true VERSIOND_ROUTER_ALLOW_COARSE_READINESS=$$off \
			VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
			VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
			VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
			VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
			VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bad.versions.map" \
			./entrypoint.sh 2>/dev/null; then \
			fail "override '$$off' spells false and must not unlock coarse readiness"; \
		fi; \
	done; \
	for garbage in maybe tru 2; do \
		if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
			GONKA_HA=true VERSIOND_ROUTER_ALLOW_COARSE_READINESS=$$garbage \
			VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
			VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
			VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
			VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
			VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bad.versions.map" \
			./entrypoint.sh 2>"$$tmpdir/boolerr"; then \
			fail "override '$$garbage' is outside the boolean grammar and was accepted"; \
		fi; \
		grep -q "not a boolean" "$$tmpdir/boolerr" \
			|| fail "the refusal for '$$garbage' does not name the boolean grammar"; \
	done; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_NON_HA_VERSIONS='v1' \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
		VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bad.versions.map" \
		./entrypoint.sh 2>/dev/null; then \
		fail "legacy versions without an explicit owner were accepted; the pool alias is not one host"; \
	fi; \
	for badhost in 'a&b' 'a|b' 'a b'; do \
		if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
			VERSIOND_POOL_HOST="$$badhost" \
			VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
			VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
			VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
			VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
			VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bad.versions.map" \
			./entrypoint.sh 2>/dev/null; then \
			fail "hostname '$$badhost' would be mangled by the renderer and was accepted"; \
		fi; \
	done; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		GONKA_HA=nonsense \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
		VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bad.versions.map" \
		./entrypoint.sh 2>/dev/null; then \
		fail "a non-boolean GONKA_HA was accepted"; \
	fi; \
	render_bool() { \
		GONKA_HA="$$1" HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_VERSIONS=v4 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bool.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bool.map" \
		VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bool.versions.map" \
		./entrypoint.sh; \
	}; \
	for on in 1 t true yes on ' ON '; do \
		render_bool "$$on" || fail "GONKA_HA='$$on' was not accepted as true"; \
		grep -Eq '^[[:space:]]*http-request set-header Devshard-Ha true[[:space:]]*$$' "$$tmpdir/bool.cfg" \
			|| fail "GONKA_HA='$$on' did not enable the HA declaration"; \
	done; \
	for off in 0 f false no off ' OFF '; do \
		render_bool "$$off" || fail "GONKA_HA='$$off' was not accepted as false"; \
		grep -Fq 'http-request set-header Devshard-Ha true if { nbsrv(versiond_ha_pool) gt 1 }' "$$tmpdir/bool.cfg" \
			|| fail "GONKA_HA='$$off' lost the runtime multi-host guard"; \
		grep -Fq 'http-request set-header Devshard-Ha true if { nbsrv(versiond_pool_v4) gt 1 }' "$$tmpdir/bool.cfg" \
			|| fail "GONKA_HA='$$off' lost the per-version runtime guard"; \
		if grep -Eq '^[[:space:]]*http-request set-header Devshard-Ha true[[:space:]]*$$' "$$tmpdir/bool.cfg"; then \
			fail "GONKA_HA='$$off' enabled the unconditional HA declaration"; \
		fi; \
	done; \
	HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
	GONKA_HA=true VERSIOND_ROUTER_ALLOW_COARSE_READINESS=1 \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/coarse.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/coarse.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/coarse.versions.map" \
	./entrypoint.sh \
		|| fail "the named coarse-readiness override was refused"; \
	HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/independent-timeouts.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/independent-timeouts.map" \
		VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/independent-timeouts.versions.map" \
		VERSIOND_ROUTER_STREAM_IDLE_SECONDS=1200 \
		VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS=60 \
		./entrypoint.sh \
			|| fail "independent SSE and upgraded-connection timeouts were rejected"; \
	grep -Eq '^[[:space:]]*timeout client[[:space:]]+1200s([[:space:]]|$$)' "$$tmpdir/independent-timeouts.cfg" \
		|| fail "SSE client idle timeout was not rendered"; \
	grep -Eq '^[[:space:]]*timeout tunnel[[:space:]]+60s([[:space:]]|$$)' "$$tmpdir/independent-timeouts.cfg" \
		|| fail "upgraded-connection timeout was not rendered independently"; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
		VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bad.versions.map" \
		VERSIOND_PORT=invalid \
		./entrypoint.sh 2>/dev/null; then \
		fail "a non-numeric port was accepted"; \
	fi; \
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad-dns.cfg" \
		HAPROXY_DNS_RESOLVER='dns service:53' \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "an unsafe DNS resolver token was accepted"; \
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
		.. \
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
		..
