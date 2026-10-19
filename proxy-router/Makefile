.PHONY: all build-docker release test-render test-route-status test-h2-upstream-check test-rpc-h2-cert test-compose test-routing

include ../scripts/blst-portable.mk

HAPROXY_IMAGE ?= haproxy:3.2-alpine

test-route-status:
	@./route-status_test.sh

test-h2-upstream-check:
	@./h2-upstream-check_test.sh

test-rpc-h2-cert:
	@./rpc-h2-cert_test.sh

test-render: test-route-status test-rpc-h2-cert
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	trap 'rm -rf "$$tmpdir"' EXIT; \
	exhausted="$$(pwd)/grpc-exhausted.http"; \
	fail() { echo "test-render: $$1" >&2; exit 1; }; \
	if ! command -v getent >/dev/null 2>&1; then \
		mkdir -p "$$tmpdir/bin"; \
		printf '%s\n' '#!/bin/sh' \
			'if [ "$$1" = ahostsv4 ] && [ "$$2" = localhost ]; then echo "127.0.0.1 STREAM localhost"; exit 0; fi' \
			'echo "getent stub: $$*" >&2; exit 2' > "$$tmpdir/bin/getent"; \
		printf '%s\n' '#!/bin/sh' 'printf "%s\n" "1: lo inet 127.0.0.1/8"' > "$$tmpdir/bin/ip"; \
		chmod +x "$$tmpdir/bin/getent" "$$tmpdir/bin/ip"; \
		PATH="$$tmpdir/bin:$$PATH"; \
		export PATH; \
	fi; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/proxy.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/versions.map" \
	HAPROXY_DNS_RESOLVER=10.96.0.10:53 \
	PROXY_ROUTER_CATALOG_BIND_HOST=localhost \
	PROXY_ROUTER_CATALOG_UPSTREAM_HOST=api \
	PROXY_ROUTER_PROXY_PROTOCOL_FROM= \
	VERSIOND_NON_HA_VERSIONS='v1 v2 v3' \
	VERSIOND_VERSIONS='v4 v5' \
	./entrypoint.sh; \
	grep -q 'server-template policy 4 proxy-policy:80 .*send-proxy-v2' "$$tmpdir/proxy.cfg" \
		|| fail "public HTTP does not use the PROXY-protocol policy pool"; \
	grep -q 'server-template policy 4 proxy-policy:443 .*send-proxy-v2' "$$tmpdir/proxy.cfg" \
		|| fail "public HTTPS does not use the PROXY-protocol policy pool"; \
	grep -q '^ *tcp-check connect port 80 send-proxy$$' "$$tmpdir/proxy.cfg" \
		|| fail "HTTP policy health does not exercise the production PROXY v2 listener"; \
	grep -q '^ *tcp-check connect port 443 ssl send-proxy$$' "$$tmpdir/proxy.cfg" \
		|| fail "HTTPS policy health does not exercise the production PROXY v2 listener"; \
	if grep -q '^ *tcp-check connect port 8081$$' "$$tmpdir/proxy.cfg"; then \
		fail "policy health can be satisfied by a connection other than the data listener"; \
	fi; \
	[ "$$(grep -c '^ *tcp-check send-binary 474554202f6865616c7468' "$$tmpdir/proxy.cfg")" -eq 2 ] \
		|| fail "policy readiness endpoint is not checked"; \
	[ "$$(grep -c '^ *option redispatch$$' "$$tmpdir/proxy.cfg")" -eq 2 ] \
		|| fail "connection failures are not redispatched across both policy pools"; \
	[ "$$(grep -c '^ *maxconn 8192$$' "$$tmpdir/proxy.cfg")" -eq 3 ] \
		|| fail "public and versiond frontends do not have an explicit connection budget"; \
	grep -q '^ *option redispatch 1$$' "$$tmpdir/proxy.cfg" \
		|| fail "versiond router pools lost owner-preserving redispatch"; \
	[ "$$(grep -c 'server-template policy .*init-state fully-down' "$$tmpdir/proxy.cfg")" -eq 2 ] \
		|| fail "policy workers can be admitted before their first successful check"; \
	grep -q '^ *timeout client 86400s$$' "$$tmpdir/proxy.cfg" \
		|| fail "public TCP client idle timeout does not cover upgraded streams"; \
	grep -q '^ *timeout server 86400s$$' "$$tmpdir/proxy.cfg" \
		|| fail "public TCP backend idle timeout does not cover upgraded streams"; \
	grep -q '^v5 versiond_routers_v5$$' "$$tmpdir/versions.map" \
		|| fail "declared versions are not mapped to route-aware router pools"; \
	if grep -q '^version=' "$$tmpdir/versions.map"; then \
		fail "readiness introduced a second projection key namespace"; \
	fi; \
	grep -q '^backend versiond_routers_v5$$' "$$tmpdir/proxy.cfg" \
		|| fail "v5 router backend was not rendered"; \
	[ "$$(grep -c '^backend versiond_routers_dynamic_[0-9]*$$' "$$tmpdir/proxy.cfg")" -eq 32 ] \
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
	grep -q 'http-check connect port 8080' "$$tmpdir/proxy.cfg" \
		|| fail "router health does not verify the data listener"; \
	grep -q 'http-check send meth GET uri /v5/healthz hdr Host versiond-router-fleet' "$$tmpdir/proxy.cfg" \
		|| fail "router data health is not version-aware"; \
	grep -q 'http-check connect port 8404' "$$tmpdir/proxy.cfg" \
		|| fail "router lifecycle health does not use the admin listener"; \
	grep -q 'http-check send meth GET uri /readyz?version=v5 hdr Host versiond-router-fleet' "$$tmpdir/proxy.cfg" \
		|| fail "router health is not version-aware"; \
	grep -q 'var(txn.ready_ver),map_str(.*versions.map) -m str versiond_routers_v5.*nbsrv(versiond_routers_v5)' "$$tmpdir/proxy.cfg" \
		|| fail "top-level version readiness is not route-aware"; \
	grep -q 'var(txn.ready_ver),map_str(.*versions.map) -m str versiond_routers_dynamic_1.*nbsrv(versiond_routers_dynamic_1)' "$$tmpdir/proxy.cfg" \
		|| fail "dynamic version readiness does not follow the live slot assignment"; \
	grep -q 'server-template router 16 versiond-router-fleet:8080 check inter' "$$tmpdir/proxy.cfg" \
		|| fail "router server template does not use the data endpoint"; \
	grep -q '^ *balance hash var(txn.hashkey)$$' "$$tmpdir/proxy.cfg" \
		|| fail "router selection is not sticky by the canonical escrow key"; \
	grep -q '^ *hash-type consistent$$' "$$tmpdir/proxy.cfg" \
		|| fail "router pools lost consistent hashing"; \
	hash_rules() { \
		awk -v front="$$1" ' \
			$$0 ~ "^frontend " front "$$" { inside = 1; next } \
			inside && $$0 ~ "^frontend |^backend " { exit } \
			inside && $$0 ~ "^ *http-request set-var\\(txn.hashkey\\)" { print } \
		' "$$2"; \
	}; \
	hash_rules versiond_router_in "$$tmpdir/proxy.cfg" > "$$tmpdir/outer-hash.rules"; \
	hash_rules rpc_h2 "$$tmpdir/proxy.cfg" > "$$tmpdir/rpc-hash.rules"; \
	grep '^ *http-request set-var(txn.hashkey)' ../versiond-router/haproxy.cfg.template \
		> "$$tmpdir/inner-hash.rules"; \
	cmp -s "$$tmpdir/outer-hash.rules" "$$tmpdir/inner-hash.rules" \
		|| { diff -u "$$tmpdir/inner-hash.rules" "$$tmpdir/outer-hash.rules" >&2 || true; fail "outer and inner escrow placement rules diverged"; }; \
	cmp -s "$$tmpdir/rpc-hash.rules" "$$tmpdir/inner-hash.rules" \
		|| { diff -u "$$tmpdir/inner-hash.rules" "$$tmpdir/rpc-hash.rules" >&2 || true; fail "peer RPC hop does not reuse the escrow placement rules"; }; \
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
	if grep -Eq 'edge_api|EDGE_API|18082' "$$tmpdir/proxy.cfg"; then \
		fail "public distributor unexpectedly owns edge-api routing"; \
	fi; \
	grep -q '^ *bind 127.0.0.1:8404$$' "$$tmpdir/proxy.cfg" \
		|| fail "admin listener is not loopback-only"; \
	if awk ' \
		$$0 ~ "^frontend versiond_router_in$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /set-header X-Real-IP/ { found = 1 } \
		END { exit !found } \
	' "$$tmpdir/proxy.cfg"; then \
		fail "parent distributor overwrites policy-derived client IP"; \
	fi; \
	grep -q '^frontend rpc_h2$$' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC frontend was not rendered"; \
	grep -q '^ *bind abns@rpc_h2c accept-proxy proto h2$$' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC frontend does not take cleartext HTTP/2 from the public :80 hand-off"; \
	if grep -q '^ *bind :9443' "$$tmpdir/proxy.cfg" || grep -q '^ *bind abns@rpc_tls' "$$tmpdir/proxy.cfg"; then \
		fail "HTTP mode rendered a separate or TLS peer RPC listen"; \
	fi; \
	awk ' \
		$$0 ~ "^frontend public_http$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /tcp-request inspect-delay 5s/ { delay = 1 } \
		inside && /acl peer_rpc_h2c req.payload\(0,24\) -m bin 505249202a20485454502f322e300d0a0d0a534d0d0a0d0a/ { acl = 1 } \
		inside && /acl peer_rpc_h2c_prefix req.payload\(0,14\) -m bin 505249202a20485454502f322e30/ { prefix = 1 } \
		inside && /tcp-request content accept if peer_rpc_h2c$$/ { accept = 1 } \
		inside && /tcp-request content accept if HTTP !peer_rpc_h2c_prefix/ { accepth1 = 1 } \
		inside && /use_backend rpc_h2c_in if peer_rpc_h2c/ { route = 1 } \
		inside && /default_backend policy_http/ { json = 1 } \
		END { exit !(delay && acl && prefix && accept && accepth1 && route && json) } \
	' "$$tmpdir/proxy.cfg" \
		|| fail "public :80 does not hand the HTTP/2 preface to peer RPC and the rest to nginx"; \
	awk ' \
		$$0 ~ "^frontend public_https$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /acl peer_rpc_tls req.ssl_alpn -i gonka-rpc\/1/ { acl = 1 } \
		inside && /tcp-request content accept if \{ req.ssl_hello_type 1 \}/ { accept = 1 } \
		inside && /use_backend rpc_tls_in if peer_rpc_tls/ { route = 1 } \
		inside && /default_backend policy_https/ { json = 1 } \
		END { exit !(acl && accept && !route && json) } \
	' "$$tmpdir/proxy.cfg" \
		|| fail "public :443 in HTTP mode must not route ALPN to a TLS peer RPC listen"; \
	grep -q '^ *server local abns@rpc_h2c send-proxy-v2$$' "$$tmpdir/proxy.cfg" \
		|| fail "the :80 hand-off does not carry the client address with PROXY v2"; \
	grep -q '^ *default_backend rpc_h1_fallback$$' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC listen is forwarded to proxy-policy"; \
	grep -q '^ *server versiond versiond:8080 proto h2 check inter 1s fall 1 rise 2 resolvers docker init-addr none$$' "$$tmpdir/proxy.cfg" \
		|| fail "non-HA peer RPC does not dial versiond:8080 proto h2 with a check"; \
	awk ' \
		$$0 ~ "^backend rpc_h2_upstream$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /http-check connect port 8080 proto h2/ { connect = 1 } \
		inside && /http-check send meth GET uri \/readyz\?peer-rpc=1 hdr Host versiond/ { send = 1 } \
		inside && /http-check expect string peer-rpc-ok/ { capability = 1 } \
		inside && /http-check expect status 200/ { expect = 1 } \
		END { exit !(connect && send && expect && capability) } \
	' "$$tmpdir/proxy.cfg" \
		|| fail "non-HA peer RPC check does not open versiond over HTTP/2"; \
	grep -q '^ *default_backend rpc_versiond_h1$$' "$$tmpdir/proxy.cfg" \
		|| fail "non-HA peer RPC does not fall back to versiond over HTTP/1.1 when its pool is empty"; \
	awk ' \
		$$0 ~ "^backend rpc_versiond_h1$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /^ *server versiond versiond:8080 resolvers docker init-addr none$$/ { h1 = 1 } \
		END { exit !h1 } \
	' "$$tmpdir/proxy.cfg" \
		|| fail "non-HA HTTP/1.1 fallback backend does not dial versiond"; \
	if awk ' \
		$$0 ~ "^backend (rpc_h2_upstream|rpc_versiond_h1)$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { inside = 0 } \
		inside && /versiond-router-fleet/ { found = 1 } \
		END { exit !found } \
	' "$$tmpdir/proxy.cfg"; then \
		fail "non-HA peer RPC references the router fleet"; \
	fi; \
	grep -q 'tcp-request content reject if peer_rpc_h2c { sc_conn_rate(0) gt 200 }' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC hand-off has no conn_rate on src"; \
	awk ' \
		$$0 ~ "^backend rpc_h2c_in$$|^backend rpc_tls_in$$" { inside = 1; n++; next } \
		inside && $$0 ~ "^frontend |^backend " { inside = 0 } \
		inside && /^ *retries 0$$/ { ok++ } \
		END { exit !(n == 2 && ok == 2) } \
	' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC hand-off backends must not retry a rejected session"; \
	grep -q 'store conn_cur,conn_rate(1s)$$' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC stick-table does not store conn_cur"; \
	grep -q 'tcp-request content reject if peer_rpc_h2c { sc_conn_cur(0) gt 32 }' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC hand-off has no default per-source connection cap"; \
	grep -q 'stick-table type ipv6 size 1m expire 10s store conn_cur,conn_rate(1s)$$' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC stick-table does not store native IPv6"; \
	if grep -q 'trusted_external_proxy' "$$tmpdir/proxy.cfg"; then \
		fail "direct mode rendered a trusted-proxy ACL"; \
	fi; \
	for fe in public_http; do \
		awk -v fe="$$fe" ' \
			$$0 == "frontend " fe { inside = 1; next } \
			inside && $$0 ~ "^frontend |^backend " { exit } \
			inside && /tcp-request content track-sc0 src,ipmask\(32,64\) table rpc_h2 if peer_rpc_/ { track = 1 } \
			inside && /tcp-request content reject if peer_rpc_[a-z0-9]+ \{ sc_conn_cur\(0\) gt / && track { cap = 1 } \
			inside && /tcp-request content accept if peer_rpc/ && !cap { early = 1 } \
			END { exit !(track && cap && !early) } \
		' "$$tmpdir/proxy.cfg" \
			|| fail "$$fe does not cap peer RPC connections per address before the hand-off"; \
	done; \
	awk ' \
		$$0 ~ "^frontend rpc_h2$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /tcp-request (connection|session|content) track-sc0/ { bad = 1 } \
		END { exit bad } \
	' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC listen tracks a connection the public frontend already counted"; \
	! grep -q 'tcp-request content reject if peer_rpc_tls { sc_conn_rate(0) gt 200 }' "$$tmpdir/proxy.cfg" \
		|| fail "HTTP-only :443 must not consume peer RPC budget"; \
	grep -q 'timeout client-hs 5s' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC listen does not bound a stalled handshake"; \
	grep -q 'track-sc1 var(txn.rpc_src) table st_rpc_watch' "$$tmpdir/proxy.cfg" \
		|| fail "path zones are not keyed per address or IPv6 /64"; \
	grep -q 'stick-table type ipv6 size 1m expire 10s store http_req_rate' "$$tmpdir/proxy.cfg" \
		|| fail "path-zone tables do not store native IPv6"; \
	awk ' \
		$$0 ~ "^frontend rpc_h2$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /set-header X-Real-IP %\[src\]/ { found = 1 } \
		END { exit !found } \
	' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC listen does not stamp X-Real-IP from src"; \
	awk ' \
		$$0 ~ "^frontend rpc_h2$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /path-strip-dotdot/ { dotdot = 1 } \
		inside && /table st_rpc_watch if peer_rpc is_watch/ { watch = 1 } \
		END { exit !(dotdot && watch) } \
	' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC listen does not strip dot-dot or Watch has no zone"; \
	grep -F -q 'deny deny_status 404 unless peer_rpc' "$$tmpdir/proxy.cfg" \
		|| fail "peer RPC listen forwards paths other than session RPC"; \
	grep -q '^backend st_rpc_watch$$' "$$tmpdir/proxy.cfg" \
		|| fail "Watch request zone was not rendered"; \
	if grep -q 'X-Versiond-Router-Addr' "$$tmpdir/proxy.cfg"; then \
		fail "public responses expose an internal upstream address"; \
	fi; \
	if grep -q 'expect-proxy' "$$tmpdir/proxy.cfg"; then \
		fail "direct ingress unexpectedly requires an incoming PROXY preamble"; \
	fi; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/proxy-lb.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/lb.map" \
	HAPROXY_DNS_RESOLVER=10.96.0.10:53 \
	PROXY_ROUTER_CATALOG_BIND_HOST=localhost \
	PROXY_ROUTER_CATALOG_UPSTREAM_HOST=api \
	PROXY_ROUTER_PROXY_PROTOCOL_FROM='10.0.0.0/8' \
	VERSIOND_NON_HA_VERSIONS='v1 v2 v3' \
	VERSIOND_VERSIONS='v4 v5' \
	./entrypoint.sh; \
	awk ' \
		$$0 ~ "^frontend rpc_h2$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /^ *bind abns@rpc_h2c accept-proxy proto h2$$/ { bind = 1 } \
		inside && /expect-proxy|trusted_external_proxy|tcp-request (connection|session|content) track-sc0/ { bad = 1 } \
		END { exit !(bind && !bad) } \
	' "$$tmpdir/proxy-lb.cfg" \
		|| fail "peer RPC listen must accept the internal PROXY v2 hand-off only, with no tracking or external preamble of its own"; \
	awk ' \
		$$0 ~ "^frontend public_http$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /acl trusted_external_proxy src 10\.0\.0\.0\/8/ { acl = 1 } \
		inside && /expect-proxy layer4 if trusted_external_proxy/ { expect = 1 } \
		inside && /tcp-request content track-sc0 src,ipmask\(32,64\) table rpc_h2 if peer_rpc_h2c/ && expect { track = 1 } \
		inside && /use_backend rpc_h2c_in if peer_rpc_h2c/ && track { after = 1 } \
		END { exit !(acl && expect && track && after) } \
	' "$$tmpdir/proxy-lb.cfg" \
		|| fail "public :80 does not consume the external PROXY preamble before keying and handing off peer RPC"; \
	docker run --rm --user "$$(id -u):$$(id -g)" \
		-v "$$tmpdir:$$tmpdir:ro" \
		-v "$$(pwd)/grpc-unavailable.http:/etc/haproxy/grpc-unavailable.http:ro" -v "$$exhausted:/etc/haproxy/grpc-exhausted.http:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f "$$tmpdir/proxy-lb.cfg" >/dev/null \
		|| fail "rendered PROXY-protocol proxy config is invalid"; \
	docker run --rm --user "$$(id -u):$$(id -g)" \
		-v "$$tmpdir:$$tmpdir:ro" \
		-v "$$(pwd)/grpc-unavailable.http:/etc/haproxy/grpc-unavailable.http:ro" -v "$$exhausted:/etc/haproxy/grpc-exhausted.http:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f "$$tmpdir/proxy.cfg" >/dev/null \
		|| fail "rendered proxy config is invalid"; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/ha.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/ha.map" \
	VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet \
	VERSIOND_ROUTER_FLEET_CAPACITY=16 \
	./entrypoint.sh; \
	grep -q 'server-template router 16 versiond-router-fleet:8081 proto h2 check inter 1s fall 1 rise 2 check-proto h1 resolvers docker init-addr none init-state fully-down hash-key addr' "$$tmpdir/ha.cfg" \
		|| fail "HA peer RPC does not dial the router peer-RPC listen :8081 proto h2 with an HTTP/1.1 check"; \
	if grep -q 'versiond-router-fleet:8080 proto h2' "$$tmpdir/ha.cfg"; then \
		fail "HA peer RPC dials the JSON listen; the previous router generation refuses /rpc/ there"; \
	fi; \
	awk ' \
		$$0 ~ "^backend rpc_h2_upstream$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /http-check connect port 8404 proto h1/ { connect = 1 } \
		inside && /http-check send meth GET uri \/readyz hdr Host versiond-router-fleet/ { send = 1 } \
		inside && /http-check expect status 200/ { expect = 1 } \
		inside && /http-check connect port 8081 proto h2/ { data = 1 } \
		inside && /http-check send meth GET uri \/healthz hdr Host versiond-router-fleet/ { dsend = 1 } \
		inside && /http-check expect status 200/ { dexpect = 1 } \
		END { exit !(connect && send && expect && data && dsend && dexpect) } \
	' "$$tmpdir/ha.cfg" \
		|| fail "HA coarse peer RPC check is not admin /readyz over HTTP/1.1 followed by RPC readiness on :8081 over HTTP/2"; \
	grep -q '^ *default_backend versiond_router_coarse$$' "$$tmpdir/ha.cfg" \
		|| fail "HA peer RPC does not fall back to the coarse JSON pool when its pool is empty"; \
	grep -q 'use_backend %\[var(txn.rpc_backend)\] if peer_rpc { var(txn.rpc_backend),nbsrv gt 0 }' "$$tmpdir/ha.cfg" \
		|| fail "peer RPC does not select the per-version _rpc backend only while it has a usable server"; \
	grep -q 'use_backend %\[var(txn.ver),map_str(.*/ha.map)\] if !versionless_request { var(txn.ver),map_str(.*/ha.map) -m found }' "$$tmpdir/ha.cfg" \
		|| fail "peer RPC does not fall back to the version's JSON backend"; \
	awk '/^backend versiond_routers_dynamic_1_rpc$$/{inside=1; next} /^backend /{inside=0} \
		inside && /http-check connect port 8081 proto h2/ { data = 1 } \
		inside && /uri-lf \/%\[be_name,regsub\(_rpc\$$,\),map\(.*\)\]\/healthz/ { dsend = 1 } \
		END { exit !(data && dsend) }' "$$tmpdir/ha.cfg" \
		|| fail "dynamic peer RPC twins do not check their version's /healthz on :8081"; \
	awk '/^backend versiond_routers_dynamic_1_rpc$$/{inside=1; next} /^backend /{inside=0} \
		inside && /server-template router 16 versiond-router-fleet:8081 proto h2/ { dial = 1 } \
		inside && /regsub\(_rpc/ { check = 1 } \
		inside && /disabled$$/ { down = 1 } \
		END { exit !(dial && check && down) }' "$$tmpdir/ha.cfg" \
		|| fail "dynamic peer RPC backends are not version-scoped proto h2 pools"; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/ha-v5.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/ha-v5.map" \
	VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet \
	VERSIOND_VERSIONS=v5 \
	./entrypoint.sh; \
	awk '/^backend versiond_routers_v5_rpc$$/{inside=1; next} /^backend /{inside=0} \
		inside && /http-check send meth GET uri \/readyz\?version=v5 hdr Host versiond-router-fleet/ { found = 1 } \
		inside && /disabled$$/ { bad = 1 } \
		END { exit !found || bad }' "$$tmpdir/ha-v5.cfg" \
		|| fail "static peer RPC backend does not check /readyz?version=v5"; \
	docker run --rm --user "$$(id -u):$$(id -g)" \
		-v "$$tmpdir:$$tmpdir:ro" \
		-v "$$(pwd)/grpc-unavailable.http:/etc/haproxy/grpc-unavailable.http:ro" -v "$$exhausted:/etc/haproxy/grpc-exhausted.http:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f "$$tmpdir/ha-v5.cfg" >/dev/null \
		|| fail "rendered versioned HA proxy config is invalid"; \
	if grep -q 'use_backend %\[var(txn.ver),map_str(.*),concat(_rpc)\]' "$$tmpdir/proxy.cfg"; then \
		fail "non-HA peer RPC selects a version pool"; \
	fi; \
	docker run --rm --user "$$(id -u):$$(id -g)" \
		-v "$$tmpdir:$$tmpdir:ro" \
		-v "$$(pwd)/grpc-unavailable.http:/etc/haproxy/grpc-unavailable.http:ro" -v "$$exhausted:/etc/haproxy/grpc-exhausted.http:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f "$$tmpdir/ha.cfg" >/dev/null \
		|| fail "rendered HA proxy config is invalid"; \
	if grep -q 'server versiond versiond:8080 proto h2' "$$tmpdir/ha.cfg"; then \
		fail "HA peer RPC still dials the non-HA versiond"; \
	fi; \
	awk '/^backend versiond_routers_v5_rpc$$/{inside=1; next} /^backend /{inside=0} \
		inside && /http-check connect port 8081 proto h2/ { data = 1 } \
		inside && /http-check send meth GET uri \/v5\/healthz hdr Host versiond-router-fleet/ { dsend = 1 } \
		END { exit !(data && dsend) }' "$$tmpdir/ha-v5.cfg" \
		|| fail "the static v5 peer RPC twin does not check /v5/healthz on :8081 over HTTP/2"; \
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/https-missing.cfg" \
		PROXY_ROUTER_VERSION_MAP="$$tmpdir/https-missing.map" \
		NGINX_MODE=https \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "HTTPS peer RPC listen started without SSL_CERT_SOURCE"; \
	fi; \
	mkdir -p "$$tmpdir/ssl"; \
	openssl req -x509 -newkey rsa:2048 -keyout "$$tmpdir/ssl/private.key" \
		-out "$$tmpdir/ssl/cert.pem" -days 1 -nodes -subj '/CN=rpc-h2.test' >/dev/null 2>&1 \
		|| fail "could not mint a throwaway TLS cert"; \
	DEVSHARD_RPC_H2_CERT_DIR="$$tmpdir/ssl"; \
	DEVSHARD_RPC_H2_PEM="$$tmpdir/rpc-h2.pem"; \
	export DEVSHARD_RPC_H2_CERT_DIR DEVSHARD_RPC_H2_PEM; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/https.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/https.map" \
	NGINX_MODE=https \
	DEVSHARD_RPC_H2_CERT_DIR="$$tmpdir/ssl" \
	DEVSHARD_RPC_H2_PEM="$$tmpdir/rpc-h2.pem" \
	./entrypoint.sh; \
	grep -q "^ *bind abns@rpc_tls accept-proxy ssl crt $$tmpdir/rpc-h2.pem alpn gonka-rpc/1 proto h2$$" "$$tmpdir/https.cfg" \
		|| fail "HTTPS peer RPC listen is not TLS with the peer RPC ALPN and HTTP/2"; \
	grep -q '^ *bind abns@rpc_h2c accept-proxy proto h2$$' "$$tmpdir/https.cfg" \
		|| fail "HTTPS mode dropped the cleartext peer RPC hand-off from :80"; \
	grep -q '^ *use_backend rpc_tls_in if peer_rpc_tls$$' "$$tmpdir/https.cfg" \
		|| fail "public :443 does not route the peer RPC ALPN to the TLS peer RPC listen"; \
	awk ' \
		$$0 ~ "^frontend public_https$$" { inside = 1; next } \
		inside && $$0 ~ "^frontend |^backend " { exit } \
		inside && /tcp-request content track-sc0 src,ipmask\(32,64\) table rpc_h2 if peer_rpc_tls/ { track = 1 } \
		inside && /use_backend rpc_tls_in if peer_rpc_tls/ && track { after = 1 } \
		END { exit !(track && after) } \
	' "$$tmpdir/https.cfg" \
		|| fail "public :443 hands off peer RPC before counting it"; \
	if grep -q 'alpn h2' "$$tmpdir/https.cfg"; then \
		fail "a peer RPC listen offers plain h2: a browser ClientHello could land there"; \
	fi; \
	docker run --rm --user "$$(id -u):$$(id -g)" \
		-v "$$tmpdir:$$tmpdir:ro" \
		-v "$$(pwd)/grpc-unavailable.http:/etc/haproxy/grpc-unavailable.http:ro" -v "$$exhausted:/etc/haproxy/grpc-exhausted.http:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f "$$tmpdir/https.cfg" >/dev/null \
		|| fail "rendered HTTPS proxy config is invalid"; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/conn-cap.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/conn-cap.map" \
	PROXY_ROUTER_RPC_MAX_CONNS_PER_SRC=64 \
	./entrypoint.sh; \
	grep -q 'tcp-request content reject if peer_rpc_h2c { sc_conn_cur(0) gt 64 }' "$$tmpdir/conn-cap.cfg" \
		|| fail "PROXY_ROUTER_RPC_MAX_CONNS_PER_SRC is not rendered on :80"; \
	! grep -q 'tcp-request content reject if peer_rpc_tls { sc_conn_cur(0) gt 64 }' "$$tmpdir/conn-cap.cfg" \
		|| fail "HTTP-only :443 must not account RPC connections"; \
	if grep -q 'sc_conn_cur(0) gt 32' "$$tmpdir/conn-cap.cfg"; then \
		fail "a default per-source connection cap survived the override"; \
	fi; \
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/conn-cap-zero.cfg" \
		PROXY_ROUTER_VERSION_MAP="$$tmpdir/conn-cap-zero.map" \
		PROXY_ROUTER_RPC_MAX_CONNS_PER_SRC=0 \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "a zero per-source connection cap was accepted"; \
	fi; \
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
	PROXY_ROUTER_OUT="$$tmpdir/path-safe.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/path-safe.map" \
	VERSIOND_VERSIONS='v1:beta' \
	./entrypoint.sh; \
	grep -q '^v1:beta versiond_routers_v1_beta_' "$$tmpdir/path-safe.map" \
		|| fail "a path-safe static version accepted by the inner router was rejected"; \
	grep -q 'uri /v1%3Abeta/healthz' "$$tmpdir/path-safe.cfg" \
		|| fail "path-safe static version health URI was not encoded"; \
	docker run --rm --user "$$(id -u):$$(id -g)" \
		-v "$$tmpdir:$$tmpdir:ro" \
		-v "$$(pwd)/grpc-unavailable.http:/etc/haproxy/grpc-unavailable.http:ro" -v "$$exhausted:/etc/haproxy/grpc-exhausted.http:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f "$$tmpdir/path-safe.cfg" >/dev/null \
		|| fail "path-safe static version produced invalid HAProxy config"; \
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/path-unsafe.cfg" \
		VERSIOND_VERSIONS='v1/beta' \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "a static version that cannot be a literal path segment was accepted"; \
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
	PROXY_ROUTER_OUT="$$tmpdir/legacy.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/legacy.map" \
	VERSIOND_ROUTER_HEALTH_CONTRACT=legacy VERSIOND_VERSIONS=v5 \
	./entrypoint.sh; \
	grep -q 'http-check send meth GET uri /v5/healthz hdr Host versiond-router-fleet' "$$tmpdir/legacy.cfg" \
		|| fail "legacy contract does not use the version data health endpoint"; \
	if grep -q 'http-check connect port 8404' "$$tmpdir/legacy.cfg"; then \
		fail "legacy contract requires an admin listener"; \
	fi; \
	HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/auto-legacy.cfg" \
		PROXY_ROUTER_VERSION_MAP="$$tmpdir/auto-legacy.map" \
		VERSIOND_ROUTER_HEALTH_CONTRACT=auto VERSIOND_VERSIONS=v5 \
		./entrypoint.sh; \
	if grep -q 'http-check connect port 8404' "$$tmpdir/auto-legacy.cfg"; then \
		fail "auto health requires an admin listener before catalog activation"; \
	fi; \
	HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/auto-readyz.cfg" \
		PROXY_ROUTER_VERSION_MAP="$$tmpdir/auto-readyz.map" \
		VERSIOND_ROUTER_HEALTH_CONTRACT=auto \
		VERSIOND_ROUTING_CATALOG_URL=http://catalog/versions VERSIOND_VERSIONS=v5 \
		./entrypoint.sh; \
	grep -q 'http-check connect port 8404' "$$tmpdir/auto-readyz.cfg" \
		|| fail "auto health did not activate lifecycle readiness with the catalog"; \
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/invalid-health.cfg" \
		VERSIOND_ROUTER_HEALTH_CONTRACT=automatic \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "an unknown router health contract was accepted"; \
	fi; \
	HAPROXY_BIN=true \
	PROXY_ROUTER_RENDER_ONLY=1 \
	PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
	PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
	PROXY_ROUTER_OUT="$$tmpdir/trusted-lb.cfg" \
	PROXY_ROUTER_VERSION_MAP="$$tmpdir/trusted-lb.map" \
	PROXY_ROUTER_PROXY_PROTOCOL_FROM='192.0.2.10/32 2001:db8::/48' \
	./entrypoint.sh; \
	[ "$$(grep -c '^ *acl trusted_external_proxy src 192.0.2.10/32 2001:db8::/48$$' "$$tmpdir/trusted-lb.cfg")" -eq 2 ] \
		|| fail "trusted external LB CIDRs were not rendered on the two public listeners"; \
	[ "$$(grep -c '^ *tcp-request connection expect-proxy layer4 if trusted_external_proxy$$' "$$tmpdir/trusted-lb.cfg")" -eq 2 ] \
		|| fail "public listeners do not consume trusted PROXY preambles"; \
	docker run --rm --user "$$(id -u):$$(id -g)" \
		-v "$$tmpdir:$$tmpdir:ro" \
		-v "$$(pwd)/grpc-unavailable.http:/etc/haproxy/grpc-unavailable.http:ro" -v "$$exhausted:/etc/haproxy/grpc-exhausted.http:ro" $(HAPROXY_IMAGE) \
		haproxy -c -f "$$tmpdir/trusted-lb.cfg" >/dev/null \
		|| fail "trusted external LB config is invalid"; \
	if HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
		PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
		PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
		PROXY_ROUTER_OUT="$$tmpdir/invalid-lb.cfg" \
		PROXY_ROUTER_PROXY_PROTOCOL_FROM='192.0.2.1/32;shutdown' \
		./entrypoint.sh >/dev/null 2>&1; then \
		fail "an unsafe trusted LB CIDR was accepted"; \
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

test-compose:
	@set -eu; \
	root=$$(cd .. && pwd); \
	edge=$$(docker compose \
		-f "$$root/deploy/join/docker-compose.yml" \
		-f "$$root/deploy/join/docker-compose.edge-api-multi.yml" \
		config --format json 2>/dev/null); \
	printf '%s\n' "$$edge" | jq -e '.services["edge-api-router"] != null and .services["proxy-policy"].environment.EDGE_API_SERVICE_NAME == "edge-api-router" and .services["proxy-policy"].environment.EDGE_API_PORT == "18080" and .services["proxy-policy2"].environment.EDGE_API_SERVICE_NAME == "edge-api-router" and .services["proxy-policy2"].environment.EDGE_API_PORT == "18080" and .services["proxy-policy"].environment.PROXY_POLICY_READINESS_HOST == "proxy-policy-app-network" and .services["proxy-policy"].networks.default.aliases == ["proxy-policy-app-network"] and .services["proxy-policy"].stop_grace_period == "30m0s" and .services["proxy-policy2"].stop_grace_period == "30m0s" and (.services.proxy.environment | has("EDGE_API_POOL_HOST") | not) and (.services.proxy.environment | has("PROXY_EDGE_API_PORT") | not)' >/dev/null || { echo "test-compose: legacy edge-api router contract changed" >&2; exit 1; }; \
	short_grace=$$(PROXY_POLICY_STOP_GRACE_PERIOD=25m docker compose \
		-f "$$root/deploy/join/docker-compose.yml" config --format json); \
	printf '%s\n' "$$short_grace" | jq -e '.services["proxy-policy"].stop_grace_period == "25m0s" and .services["proxy-policy2"].stop_grace_period == "25m0s"' >/dev/null || { echo "test-compose: policy stop grace override was not applied" >&2; exit 1; }; \
	printf '%s\n' "$$edge" | jq -e '((.services.proxy.depends_on // {}) | has("proxy-policy") | not) and ((.services.proxy.depends_on // {}) | has("proxy-policy2") | not) and ([.services["proxy-policy"], .services["proxy-policy2"]] | all(.depends_on.api.condition == "service_started" and .depends_on.versiond.condition == "service_started" and .depends_on["proxy-ssl"].condition == "service_healthy" and .depends_on["proxy-ssl"].required == false and (.depends_on | has("proxy-policy2") | not)))' >/dev/null || { echo "test-compose: policy workers no longer start independently behind an ungated HAProxy" >&2; exit 1; }; \
	versiond=$$(DEVSHARD_POSTGRES_PASSWORD=test docker compose \
		-f "$$root/deploy/join/docker-compose.yml" \
		-f "$$root/deploy/join/docker-compose.versiond.yml" \
		config --format json 2>/dev/null); \
	printf '%s\n' "$$versiond" | jq -e '.services["proxy-policy"].environment.VERSIOND_SERVICE_NAME == "proxy-policy-ingress" and .services["proxy-policy"].environment.VERSIOND_PORT == "18081" and .services["proxy-policy2"].environment.VERSIOND_SERVICE_NAME == "proxy-policy-ingress" and .services["proxy-policy2"].environment.VERSIOND_PORT == "18081" and .services.proxy.environment.VERSIOND_ROUTER_POOL_HOST == "versiond-router-fleet" and .services.proxy.environment.VERSIOND_ROUTER_PORT == "8080" and .services.proxy.environment.VERSIOND_ROUTER_ADMIN_PORT == "8404" and .services.proxy.environment.VERSIOND_ROUTER_FLEET_CAPACITY == "16" and (.services.proxy.environment | has("VERSIOND_ROUTER_HEALTH_CONTRACT") | not) and (.services | has("versiond-router") | not) and (.services.proxy.networks | has("versiond-router-front")) and (.services.proxy.networks | has("versiond-router-back"))' >/dev/null || { echo "test-compose: versiond fleet distributor contract changed" >&2; exit 1; }; \
	printf '%s\n' "$$versiond" | jq -e '((.services.proxy.depends_on // {}) | has("versiond-router") | not)' >/dev/null || { echo "test-compose: inner router delays public proxy startup" >&2; exit 1; }; \
	printf '%s\n' "$$versiond" | jq -e '([.services["proxy-policy"], .services["proxy-policy2"]] | all(((.depends_on // {}) | has("versiond") | not) and .depends_on.api.condition == "service_started" and .depends_on["proxy-ssl"].required == false))' >/dev/null || { echo "test-compose: HA versiond or PostgreSQL delays policy startup" >&2; exit 1; }; \
	head -n 14 "$$root/deploy/join/docker-compose.versiond.yml" | grep -q 'Fresh installations only' && \
	head -n 14 "$$root/deploy/join/docker-compose.versiond.yml" | grep -q 'Compose 2.24.4 or newer' && \
	head -n 14 "$$root/deploy/join/docker-compose.versiond.yml" | grep -q 'release updater' \
		|| { echo "test-compose: direct HA overlay command lacks its install and upgrade constraints" >&2; exit 1; }; \
	printf '%s\n' "$$versiond" | jq -e '.networks["versiond-router-front"].external == true and .networks["versiond-router-back"].external == true and (.services.versiond.networks["versiond-router-back"].aliases | index("versiond-pool"))' >/dev/null || { echo "test-compose: independent versiond-router fleet networks are missing" >&2; exit 1; }; \
	custom_admin=$$(DEVSHARD_POSTGRES_PASSWORD=test VERSIOND_ROUTER_ADMIN_PORT=9410 docker compose \
		-f "$$root/deploy/join/docker-compose.yml" \
		-f "$$root/deploy/join/docker-compose.versiond.yml" \
		config --format json); \
	printf '%s\n' "$$custom_admin" | jq -e '.services.proxy.environment.VERSIOND_ROUTER_ADMIN_PORT == "8404"' >/dev/null || { echo "test-compose: fleet admin port can diverge across Compose projects" >&2; exit 1; }; \
	grep -A5 '^  - job_name: proxy-router$$' \
		"$$root/deploy/join/observability/prometheus.yml" \
		| grep -q 'proxy-router-metrics:8405' \
		|| { echo "test-compose: public router metrics are not scraped" >&2; exit 1; }; \
	if grep -Eq 'up -d proxy-policy proxy-policy2|up -d proxy-policy2 proxy-policy|--remove-orphans' \
		"$$root/proxy/README.md" "$$root/proxy-router/README.md"; then \
		echo "test-compose: proxy runbook contains a non-rolling or whole-project mutation" >&2; \
		exit 1; \
	fi; \
	echo "test-compose: ok"

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

.PHONY: test-rpc-upgrade
test-rpc-upgrade:
	cd ../devshard && go test -tags=ingressintegration ./testenv/ingress -count=1 -timeout=10m
