.PHONY: all build-docker release test-render test-compose-contract
.PHONY: test-hash-ring test-version-routing test-fleet

# Renders both the mixed (legacy pinning) and the all-HA shapes, asserts on the
# result, then runs the real HAProxy from the production image over each one.
# Rendering needs no HAProxy locally; the syntax check needs Docker.
HAPROXY_IMAGE ?= haproxy:3.2-alpine

test-compose-contract:
	@../deploy/join/versiond-compose-config_test.sh

test-fleet:
	@../deploy/join/versiond-router-fleet_test.sh

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
		'        self._r(200, NAME)' \
		'    def log_message(self, *a): pass' \
		'http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()' \
		> "$$tmpdir/upstream.py"; \
	mkdir "$$tmpdir/catalog"; \
	printf '%s\n' '{"schema":1,"initialized":true,"revision":1,"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"}]}' \
		> "$$tmpdir/catalog/versions.json"; \
	printf '%s\n' \
		'import http.server, json' \
		'class H(http.server.BaseHTTPRequestHandler):' \
		'    def do_GET(self):' \
		'        if self.path != "/versions": self.send_error(404); return' \
		'        body = open("/data/versions.json", "rb").read()' \
		'        self.send_response(200)' \
		'        self.send_header("Content-Type", "application/json")' \
		'        self.send_header("Content-Length", str(len(body)))' \
		'        self.end_headers(); self.wfile.write(body)' \
		'    def log_message(self, *a): pass' \
		'http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()' \
		> "$$tmpdir/catalog.py"; \
	docker network create $$net >/dev/null; \
	docker volume create $$state >/dev/null; \
	docker build -q -t gonka-versiond-router-probe \
		-f Dockerfile .. >/dev/null; \
	docker run -d --name gonka-vp-catalog --network $$net \
		--network-alias routing-catalog \
		-v "$$tmpdir/catalog:/data:ro" -v "$$tmpdir/catalog.py:/app.py:ro" \
		python:3.12-alpine python /app.py >/dev/null; \
	for spec in "a:v4:false:false" "b:v4 v5 v9 v9;hotfix v10:true:true"; do \
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
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=2 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="v1 v2" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 60); do \
		curl -s -o /dev/null -m 2 http://127.0.0.1:18132/v4/sessions/warm/x && break; \
		sleep 0.5; \
	done; \
	sleep 4; \
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
	undeclared=$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v9/sessions/a/chat); \
	[ "$$undeclared" = 503 ] \
		|| fail "an undeclared version answered $$undeclared; it must be refused, not routed by luck"; \
	curl -s -m 3 http://127.0.0.1:18132/v9/sessions/a/chat \
		| grep -q 'governance routing catalog' \
		|| fail "the refusal does not identify the authoritative catalog"; \
	health=$$(curl -s -o /dev/null -w '%{http_code}' -m 3 http://127.0.0.1:18132/healthz); \
	[ "$$health" = 200 ] \
		|| fail "the guard broke the router's own /healthz (got $$health)"; \
	router_id=$$(docker inspect -f '{{.Id}}' gonka-vp-router); \
	printf '%s\n' '{"schema":1,"initialized":true,"revision":2,"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 30); do \
		if curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9' >/dev/null; then break; fi; \
		sleep 0.5; \
	done; \
	curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9' >/dev/null \
		|| fail "governance v9 never became an admitted inner-router route"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/dynamic/chat)" = b ] \
		|| fail "dynamically learned v9 did not reach its ready host"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v9 \
		|| fail "runtime catalog does not report dynamically learned v9"; \
	[ "$$(docker inspect -f '{{.Id}}' gonka-vp-router)" = "$$router_id" ] \
		|| fail "learning v9 replaced the router container"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v4/sessions/still-live/chat)" = b ] \
		|| fail "learning v9 disrupted the existing v4 route"; \
	docker exec gonka-vp-router test -s /var/lib/gonka-router/catalog.json \
		|| fail "inner router did not persist its learned catalog"; \
	docker stop gonka-vp-catalog >/dev/null; \
	docker rm -f gonka-vp-router >/dev/null; \
	docker run -d --name gonka-vp-router --network $$net \
		-p 127.0.0.1:18132:8080 -p 127.0.0.1:18134:8404 \
		-v $$state:/var/lib/gonka-router \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		-e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
		-e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
		-e VERSIOND_ROUTER_VERSION_CAPACITY=2 \
		-e VERSIOND_LEGACY_HOST=gonka-vp-legacy \
		-e VERSIOND_NON_HA_VERSIONS="v1 v2" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 30); do \
		if curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9' >/dev/null; then break; fi; \
		sleep 0.5; \
	done; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/cache/chat)" = b ] \
		|| fail "inner router lost a learned route when restarted without dapi"; \
	docker start gonka-vp-catalog >/dev/null; \
	printf '%s\n' '{"schema":1,"initialized":true,"revision":3,"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"},{"name":"v10"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	sleep 2; \
	if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v10; then \
		fail "an invalid duplicate catalog snapshot changed the route map"; \
	fi; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/after-invalid/chat)" = b ] \
		|| fail "an invalid catalog snapshot removed the last valid route view"; \
	printf '%s\n' '{"schema":1,"initialized":false,"revision":3,"versions":[]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	sleep 2; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/after-uninitialized/chat)" = b ] \
		|| fail "an uninitialized catalog replaced the last accepted route view"; \
	printf '%s\n' '{"schema":1,"initialized":true,"revision":1,"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 10); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "revision-error"' >/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "revision-error"' >/dev/null \
		|| fail "a regressing catalog revision was not rejected"; \
	printf '%s\n' '{"schema":1,"initialized":true,"revision":3,"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v9;hotfix"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 30); do \
		if curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%3Bhotfix' >/dev/null; then break; fi; \
		sleep 0.5; \
	done; \
	curl -fsS -m 3 'http://127.0.0.1:18134/readyz?version=v9%3Bhotfix' >/dev/null \
		|| fail "encoded admin readiness did not follow the dynamic governance map"; \
	[ "$$(curl -fsS -m 3 'http://127.0.0.1:18132/v9;hotfix/sessions/escaped/chat')" = b ] \
		|| fail "a routable governance name requiring Runtime API escaping was corrupted"; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -Fqx 'v9;hotfix' \
		|| fail "runtime catalog lost the literal semicolon in a governance name"; \
	printf '%s\n' '{"schema":1,"initialized":true,"revision":4,"versions":[{"name":"v1"},{"name":"v2"},{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v9;hotfix"},{"name":"v10"}]}' \
		> "$$tmpdir/catalog/versions.next"; \
	mv "$$tmpdir/catalog/versions.next" "$$tmpdir/catalog/versions.json"; \
	for i in $$(seq 30); do \
		if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
			| jq -e '.state == "capacity-exhausted" and .dynamic_slots_used == 2' \
				>/dev/null 2>&1; then break; fi; \
		sleep 0.5; \
	done; \
	if docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status \
		/etc/haproxy/versions.map | grep -qx v10; then \
		fail "catalog capacity exhaustion silently published v10"; \
	fi; \
	docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state \
		| jq -e '.state == "capacity-exhausted" and .dynamic_slots_used == 2' \
			>/dev/null || { \
			docker exec gonka-vp-router /usr/local/lib/router-runtime/catalog-status --state >&2; \
			fail "catalog capacity exhaustion is not exposed as persistent degraded state"; \
		}; \
	[ "$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v10/sessions/capacity/chat)" = 503 ] \
		|| fail "capacity-exhausted v10 was routed without an assigned backend"; \
	[ "$$(curl -fsS -m 3 http://127.0.0.1:18132/v9/sessions/persisted/chat)" = b ] \
		|| fail "a later catalog snapshot removed an already admitted route"; \
	echo "test-version-routing: ok"

test-render: test-compose-contract test-hash-ring test-version-routing
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
	grep -q '^[[:space:]]*bind 127.0.0.1:8405' "$$tmpdir/mixed.cfg" \
		|| fail "metrics are not bound to loopback"; \
	grep -q '^[[:space:]]*option[[:space:]]*forwardfor' "$$tmpdir/mixed.cfg" \
		|| fail "X-Forwarded-For is no longer appended; the client audit trail dies at this hop"; \
	grep -q '^[[:space:]]*http-request set-header X-Real-IP' "$$tmpdir/mixed.cfg" \
		|| fail "X-Real-IP is no longer set"; \
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
		|| fail "the catalog writer does not have a dedicated restricted socket"; \
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
	printf 'v4 versiond_pool_v4\nv5 versiond_pool_v5\n' > "$$tmpdir/want.versions.map"; \
	diff -u "$$tmpdir/want.versions.map" "$$tmpdir/mixed.versions.map" \
		|| fail "declared versions rendered incorrectly"; \
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
	HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
	GONKA_HA=false VERSIOND_VERSIONS=v4 \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/haoff.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/haoff.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/haoff.versions.map" \
	./entrypoint.sh \
		|| fail "GONKA_HA=false must mean off, not on"; \
	grep -Fq 'http-request set-header Devshard-Ha true if { nbsrv(versiond_ha_pool) gt 1 }' "$$tmpdir/haoff.cfg" \
		|| fail "GONKA_HA=false lost the coarse backend runtime guard"; \
	grep -Fq 'http-request set-header Devshard-Ha true if { nbsrv(versiond_pool_v4) gt 1 }' "$$tmpdir/haoff.cfg" \
		|| fail "the v4 storage guard does not count its selected backend"; \
	if grep -Eq '^[[:space:]]*http-request set-header Devshard-Ha true[[:space:]]*$$' "$$tmpdir/haoff.cfg"; then \
		fail "GONKA_HA=false enabled the unconditional HA declaration"; \
	fi; \
	HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
	GONKA_HA=' true ' VERSIOND_VERSIONS=v4 \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/hapad.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/hapad.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/hapad.versions.map" \
	./entrypoint.sh \
		|| fail "GONKA_HA=' true ' must read as on: Go trims whitespace, so must this parser"; \
	grep -Eq '^[[:space:]]*http-request set-header Devshard-Ha true[[:space:]]*$$' "$$tmpdir/hapad.cfg" \
		|| fail "GONKA_HA=' true ' parsed but did not stamp the HA header"; \
	HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
	GONKA_HA=' no ' \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/hapadoff.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/hapadoff.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/hapadoff.versions.map" \
	./entrypoint.sh \
		|| fail "GONKA_HA=' no ' must read as off, not as garbage"; \
	grep -Fq 'http-request set-header Devshard-Ha true if { nbsrv(versiond_ha_pool) gt 1 }' "$$tmpdir/hapadoff.cfg" \
		|| fail "GONKA_HA=' no ' must leave only the runtime multi-host guard"; \
	if grep -Eq '^[[:space:]]*http-request set-header Devshard-Ha true[[:space:]]*$$' "$$tmpdir/hapadoff.cfg"; then \
		fail "GONKA_HA=' no ' enabled the unconditional HA declaration"; \
	fi; \
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
