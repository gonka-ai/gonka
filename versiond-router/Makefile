.PHONY: all build-docker release test-render test-hash-ring test-version-routing

# Renders both the mixed (legacy pinning) and the all-HA shapes, asserts on the
# result, then runs the real HAProxy from the production image over each one.
# Rendering needs no HAProxy locally; the syntax check needs Docker.
HAPROXY_IMAGE ?= haproxy:3.2-alpine

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
		done; \
		docker logs gonka-hash-probe 2>&1 \
			| awk '/^PICK .*sessions/ { print $$2, $$3 }' | sort; \
		docker rm -f gonka-hash-probe >/dev/null 2>&1 || true; \
	}; \
	probe "127.0.0.2 127.0.0.3 127.0.0.4 127.0.0.5" > "$$tmpdir/forward"; \
	probe "127.0.0.5 127.0.0.4 127.0.0.3 127.0.0.2" > "$$tmpdir/reversed"; \
	probe "127.0.0.4 127.0.0.2 127.0.0.5 127.0.0.3" > "$$tmpdir/shuffled"; \
	[ "$$(wc -l < "$$tmpdir/forward")" -eq 8 ] \
		|| fail "expected one routing decision per escrow, got: $$(cat "$$tmpdir/forward")"; \
	diff -u "$$tmpdir/forward" "$$tmpdir/reversed" \
		|| fail "reversing the slot order moved sessions: the ring follows the slot, not the host"; \
	diff -u "$$tmpdir/forward" "$$tmpdir/shuffled" \
		|| fail "shuffling the slot order moved sessions: the ring follows the slot, not the host"; \
	echo "test-hash-ring: ok"

# The point of per-version pools is that one host missing one version keeps
# serving the others. That is a property of the running router, not of the text
# of its config, so it is checked by routing real requests through the real
# image at two upstreams that disagree about which versions they serve.
test-version-routing:
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	net=gonka-version-probe; \
	cleanup() { \
		docker rm -f gonka-vp-router gonka-vp-a gonka-vp-b >/dev/null 2>&1 || true; \
		docker network rm $$net >/dev/null 2>&1 || true; \
		rm -rf "$$tmpdir"; \
	}; \
	trap cleanup EXIT; \
	fail() { echo "test-version-routing: $$1" >&2; exit 1; }; \
	printf '%s\n' \
		'import os, http.server, urllib.parse' \
		'NAME = os.environ["NAME"]' \
		'SERVES = set(os.environ["SERVES"].split())' \
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
		'            v = urllib.parse.parse_qs(u.query).get("version", [""])[0]' \
		'            return self._r(503 if v and v not in SERVES else 200, "x")' \
		'        if u.path.endswith("/ha-header"):' \
		'            return self._r(200, self.headers.get("Devshard-Ha", ""))' \
		'        self._r(200, NAME)' \
		'    def log_message(self, *a): pass' \
		'http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()' \
		> "$$tmpdir/upstream.py"; \
	docker network create $$net >/dev/null; \
	docker build -q -t gonka-versiond-router-probe . >/dev/null; \
	for spec in "a:v4" "b:v4 v5"; do \
		name=$${spec%%:*}; serves=$${spec#*:}; \
		docker run -d --name gonka-vp-$$name --network $$net --network-alias versiond-pool \
			-e NAME=$$name -e SERVES="$$serves" -v "$$tmpdir/upstream.py:/app.py:ro" \
			python:3.12-alpine python /app.py >/dev/null; \
	done; \
	docker run -d --name gonka-vp-router --network $$net -p 127.0.0.1:18132:8080 \
		-e VERSIOND_POOL_HOST=versiond-pool -e VERSIOND_VERSIONS="v4 v5" \
		gonka-versiond-router-probe >/dev/null; \
	for i in $$(seq 60); do \
		curl -s -o /dev/null -m 2 http://127.0.0.1:18132/v4/sessions/warm/x && break; \
		sleep 0.5; \
	done; \
	sleep 4; \
	for version in v4 v5; do \
		ha_header=$$(curl -s -m 3 "http://127.0.0.1:18132/$$version/ha-header"); \
		[ "$$ha_header" = true ] \
			|| fail "two usable hosts without GONKA_HA produced Devshard-Ha='$$ha_header' for $$version"; \
	done; \
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
	undeclared=$$(curl -s -o /dev/null -w '%{http_code}' -m 3 \
		http://127.0.0.1:18132/v9/sessions/a/chat); \
	[ "$$undeclared" = 503 ] \
		|| fail "an undeclared version answered $$undeclared; it must be refused, not routed by luck"; \
	curl -s -m 3 http://127.0.0.1:18132/v9/sessions/a/chat | grep -q VERSIOND_VERSIONS \
		|| fail "the refusal does not name the setting that fixes it"; \
	health=$$(curl -s -o /dev/null -w '%{http_code}' -m 3 http://127.0.0.1:18132/healthz); \
	[ "$$health" = 200 ] \
		|| fail "the guard broke the router's own /healthz (got $$health)"; \
	echo "test-version-routing: ok"

test-render: test-hash-ring test-version-routing
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
	VERSIOND_VERSIONS='v4,v5' \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/mixed.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/mixed.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/mixed.versions.map" \
	./entrypoint.sh; \
	grep -q '^[[:space:]]*server-template versiond 64 versiond-pool:8080' "$$tmpdir/mixed.cfg" \
		|| fail "HA pool is not a DNS-resolved server-template"; \
	grep -q '^[[:space:]]*hash-type consistent' "$$tmpdir/mixed.cfg" \
		|| fail "HA pool lost consistent hashing"; \
	grep -q '^[[:space:]]*server-template versiond .*hash-key addr' "$$tmpdir/mixed.cfg" \
		|| fail "pool servers must hash by address; by slot id a router restart re-homes every session"; \
	grep -q '^[[:space:]]*http-check send meth GET uri /readyz' "$$tmpdir/mixed.cfg" \
		|| fail "active health checks are not configured"; \
	grep -Eq '^[[:space:]]*http-request set-header Devshard-Ha true[[:space:]]*$$' "$$tmpdir/mixed.cfg" \
		|| fail "GONKA_HA did not enable the Devshard-Ha header"; \
	grep -q '^[[:space:]]*server legacy versiond:8080' "$$tmpdir/mixed.cfg" \
		|| fail "legacy backend does not point at VERSIOND_LEGACY_HOST"; \
	grep -q '^[[:space:]]*retry-on conn-failure empty-response 502' "$$tmpdir/mixed.cfg" \
		|| fail "failover no longer excludes 503"; \
	grep -q '^[[:space:]]*http-request disable-l7-retry if !METH_GET' "$$tmpdir/mixed.cfg" \
		|| fail "non-idempotent requests could be replayed"; \
	grep -q '^[[:space:]]*bind 127.0.0.1:8405' "$$tmpdir/mixed.cfg" \
		|| fail "metrics are not bound to loopback"; \
	grep -q '^[[:space:]]*option[[:space:]]*forwardfor' "$$tmpdir/mixed.cfg" \
		|| fail "X-Forwarded-For is no longer appended; the client audit trail dies at this hop"; \
	grep -q '^[[:space:]]*http-request set-header X-Real-IP' "$$tmpdir/mixed.cfg" \
		|| fail "X-Real-IP is no longer set"; \
	grep -q '^[[:space:]]*http-request deny deny_status 413 if { req.hdr_val(content-length) gt 10485760 }' "$$tmpdir/mixed.cfg" \
		|| fail "the request body cap is gone"; \
	grep -Eq '^[[:space:]]*timeout check 3s([[:space:]]|$$)' "$$tmpdir/mixed.cfg" \
		|| fail "no timeout check directive: inter would double as the whole check budget and a slow answer would read as down"; \
	grep -q '^[[:space:]]*http-check send meth GET uri /readyz?version=v4' "$$tmpdir/mixed.cfg" \
		|| fail "declared versions must be health-checked individually"; \
	grep -q '^backend versiond_pool_v5$$' "$$tmpdir/mixed.cfg" \
		|| fail "no backend for declared version v5"; \
	grep -q '^[[:space:]]*use_backend %\[var(txn.ver),map_str' "$$tmpdir/mixed.cfg" \
		|| fail "requests are not routed to their version's pool"; \
	printf 'v4 versiond_pool_v4\nv5 versiond_pool_v5\n' > "$$tmpdir/want.versions.map"; \
	diff -u "$$tmpdir/want.versions.map" "$$tmpdir/mixed.versions.map" \
		|| fail "declared versions rendered incorrectly"; \
	for backend in versiond_ha_pool versiond_pool_v4 versiond_pool_v5; do \
		count=$$(awk -v b="backend $$backend" '$$0 == b {inside=1; next} /^backend /{inside=0} \
			inside && $$0 !~ /^[[:space:]]*#/ && \
			/hash-key addr|http-reuse safe|disable-l7-retry|retry-on|hash-type consistent/ {n++} \
			END {print n + 0}' "$$tmpdir/mixed.cfg"); \
		[ "$$count" -eq 5 ] \
			|| fail "$$backend has $$count of 5 policy lines; the pools have drifted apart"; \
	done; \
	printf 'v1 legacy\nv2 legacy\nv3 legacy\n' > "$$tmpdir/want.map"; \
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
	grep -q '^[[:space:]]*http-request del-header Devshard-Ha' "$$tmpdir/allha.cfg" \
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
	GONKA_HA=false \
	VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
	VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
	VERSIOND_ROUTER_OUT="$$tmpdir/haoff.cfg" \
	VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/haoff.map" \
	VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/haoff.versions.map" \
	./entrypoint.sh \
		|| fail "GONKA_HA=false must mean off, not on"; \
	grep -Fq 'http-request set-header Devshard-Ha true if { nbsrv(versiond_ha_pool) gt 1 }' "$$tmpdir/haoff.cfg" \
		|| fail "GONKA_HA=false must leave only the runtime multi-host guard"; \
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
	if HAPROXY_BIN=true VERSIOND_ROUTER_RENDER_ONLY=1 \
		VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
		VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
		VERSIOND_ROUTER_OUT="$$tmpdir/bad.cfg" \
		VERSIOND_ROUTER_NON_HA_MAP="$$tmpdir/bad.map" \
		VERSIOND_ROUTER_VERSIONS_MAP="$$tmpdir/bad.versions.map" \
		VERSIOND_ROUTER_STREAM_IDLE_SECONDS=1200 \
		VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS=60 \
		./entrypoint.sh 2>/dev/null; then \
		fail "a tunnel timeout below the idle timeout was accepted"; \
	fi; \
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
