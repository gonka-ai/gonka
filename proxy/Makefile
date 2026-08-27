.PHONY: all build-docker release test-render

include ../scripts/blst-portable.mk

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
PLATFORMS ?= linux/amd64,linux/arm64
USE_REGISTRY_CACHE ?= 0
GHCR_CACHE_NAMESPACE ?= gonka-ai
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/proxy:buildcache --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/proxy:buildcache,mode=min
DOCKER_BUILD_PREFIX := docker buildx build --load $(DOCKER_CACHE_ARGS)
else
DOCKER_CACHE_ARGS :=
DOCKER_BUILD_PREFIX := DOCKER_BUILDKIT=1 docker build
endif

define DOCKER_BUILD
	@echo "--> building proxy docker image"
	@echo "    	platform: $(PLATFORM)"
	@$(DOCKER_BUILD_PREFIX) \
		--platform $(PLATFORM) \
		-f $(DOCKER_FILE) \
		. \
		-t $(DOCKER_TAG)
endef

all: build-docker

test-render:
	@set -eu; \
	tmpdir=$$(mktemp -d); \
	image=gonka-proxy-render-test:$$$$; \
	private=gonka-proxy-policy-private-$$$$; \
	shared=gonka-proxy-policy-shared-$$$$; \
	peer=gonka-proxy-policy-peer-$$$$; \
	worker=gonka-proxy-policy-worker-$$$$; \
	ssl_worker_a=gonka-proxy-policy-ssl-a-$$$$; \
	ssl_worker_b=gonka-proxy-policy-ssl-b-$$$$; \
	trap 'docker rm -f "$$peer" "$$worker" "$$ssl_worker_a" "$$ssl_worker_b" >/dev/null 2>&1 || true; docker network rm "$$private" "$$shared" >/dev/null 2>&1 || true; docker image rm "$$image" >/dev/null 2>&1 || true; rm -rf "$$tmpdir"' EXIT; \
	fail() { echo "test-render: $$1" >&2; exit 1; }; \
	touch "$$tmpdir/nginx.conf"; \
	docker build -q -t "$$image" . >/dev/null; \
	[ "$$(docker image inspect -f '{{index .Config.Labels "ai.gonka.proxy-policy-contract"}}' "$$image")" = 1 ] \
		|| fail "policy image has no stable ingress contract label"; \
	docker run --rm \
		-e NGINX_MODE=http \
		-e PROXY_PROTOCOL=true \
		-e PROXY_PROTOCOL_BIND_ADDRESS=127.0.0.1 \
		-e PROXY_PROTOCOL_TRUSTED_FROM=127.0.0.1/32 \
		-e KEY_NAME=node-a \
		-e VERSIOND_SERVICE_NAME=proxy \
		-e VERSIOND_SERVICE_IS_ABSOLUTE=true \
		-e VERSIOND_PORT=18081 \
		-e EDGE_API_SERVICE_NAME=edge-api-router \
		-e EDGE_API_SERVICE_IS_ABSOLUTE=true \
		-e EDGE_API_PORT=18080 \
		-v "$$tmpdir/nginx.conf:/etc/nginx/nginx.conf" \
		"$$image" true >/dev/null; \
	grep -q 'listen 127.0.0.1:80 proxy_protocol;' "$$tmpdir/nginx.conf" \
		|| fail "private policy listener does not require PROXY protocol"; \
	grep -q 'listen 127.0.0.1:8081;' "$$tmpdir/nginx.conf" \
		|| fail "loopback health listener is absent"; \
	grep -q 'real_ip_header proxy_protocol;' "$$tmpdir/nginx.conf" \
		|| fail "PROXY client address is not restored"; \
	grep -q 'set_real_ip_from 127.0.0.1/32;' "$$tmpdir/nginx.conf" \
		|| fail "PROXY client address trusts an unexpected source"; \
	grep -q 'server proxy:18081 resolve;' "$$tmpdir/nginx.conf" \
		|| fail "versiond policy route does not return to the HA distributor"; \
	grep -q 'server edge-api-router:18080 resolve;' "$$tmpdir/nginx.conf" \
		|| fail "edge-api policy route does not preserve the existing nginx router"; \
	if grep -q 'node-a-proxy:1808' "$$tmpdir/nginx.conf"; then \
		fail "internal distributor received the external KEY_NAME prefix"; \
	fi; \
	touch "$$tmpdir/private.conf"; \
	docker network create "$$private" >/dev/null; \
	docker network create "$$shared" >/dev/null; \
	docker run -d --name "$$peer" --network "$$private" \
		--network-alias proxy-policy-ingress alpine:3.21 sleep 60 >/dev/null; \
	docker create --name "$$worker" --network "$$private" \
		-e NGINX_MODE=http -e PROXY_PROTOCOL=true \
		-e PROXY_PROTOCOL_PEER=proxy-policy-ingress \
		-e KEY_NAME=node-a \
		-v "$$tmpdir/private.conf:/etc/nginx/nginx.conf" \
		"$$image" sleep 30 >/dev/null; \
	docker network connect "$$shared" "$$worker"; \
	docker start "$$worker" >/dev/null; \
	for _ in $$(seq 1 50); do \
		[ -s "$$tmpdir/private.conf" ] && break; \
		sleep 0.1; \
	done; \
	[ -s "$$tmpdir/private.conf" ] || fail "dynamic private-network render did not complete"; \
	private_ip=$$(docker inspect -f "{{with index .NetworkSettings.Networks \"$$private\"}}{{.IPAddress}}{{end}}" "$$worker"); \
	shared_ip=$$(docker inspect -f "{{with index .NetworkSettings.Networks \"$$shared\"}}{{.IPAddress}}{{end}}" "$$worker"); \
	private_subnet=$$(docker network inspect -f '{{(index .IPAM.Config 0).Subnet}}' "$$private"); \
	grep -q "listen $$private_ip:80 proxy_protocol;" "$$tmpdir/private.conf" \
		|| fail "PROXY listener is not bound to the private policy interface"; \
	grep -q "set_real_ip_from $$private_subnet;" "$$tmpdir/private.conf" \
		|| fail "PROXY trust is not restricted to the private policy network"; \
	if grep -q "listen $$shared_ip:80 proxy_protocol;" "$$tmpdir/private.conf"; then \
		fail "shared application network can reach the PROXY listener"; \
	fi; \
	mkdir "$$tmpdir/ssl"; \
	touch "$$tmpdir/ssl-a.conf" "$$tmpdir/ssl-b.conf"; \
	printf '%s\n' '#!/bin/sh' 'set -eu' \
		'printf "%s\n" called >> /etc/nginx/ssl/setup.calls' \
		'sleep 2' ': > /etc/nginx/ssl/cert.pem' \
		': > /etc/nginx/ssl/private.key' > "$$tmpdir/setup-ssl.sh"; \
	chmod +x "$$tmpdir/setup-ssl.sh"; \
	docker create --name "$$ssl_worker_a" \
		-e NGINX_MODE=https -e CERT_ISSUER_DOMAIN=example.test \
		-e PROXY_CERT_RELOAD_POLL_SECONDS=60 \
		-v "$$tmpdir/ssl:/etc/nginx/ssl" \
		-v "$$tmpdir/setup-ssl.sh:/setup-ssl.sh:ro" \
		-v "$$tmpdir/ssl-a.conf:/etc/nginx/nginx.conf" \
		"$$image" sleep 30 >/dev/null; \
	docker create --name "$$ssl_worker_b" \
		-e NGINX_MODE=https -e CERT_ISSUER_DOMAIN=example.test \
		-e PROXY_CERT_RELOAD_POLL_SECONDS=60 \
		-v "$$tmpdir/ssl:/etc/nginx/ssl" \
		-v "$$tmpdir/setup-ssl.sh:/setup-ssl.sh:ro" \
		-v "$$tmpdir/ssl-b.conf:/etc/nginx/nginx.conf" \
		"$$image" sleep 30 >/dev/null; \
	docker start "$$ssl_worker_a" "$$ssl_worker_b" >/dev/null; \
	for _ in $$(seq 1 100); do \
		[ -s "$$tmpdir/ssl-a.conf" ] && [ -s "$$tmpdir/ssl-b.conf" ] && break; \
		sleep 0.1; \
	done; \
	[ -s "$$tmpdir/ssl-a.conf" ] && [ -s "$$tmpdir/ssl-b.conf" ] \
		|| fail "concurrent TLS policy workers did not finish startup"; \
	[ "$$(wc -l < "$$tmpdir/ssl/setup.calls")" -eq 1 ] \
		|| fail "concurrent TLS policy workers placed more than one initial certificate order"; \
	echo "test-render: ok"

build-docker:
	$(eval PLATFORM=$(DOCKER_PLATFORM))
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=ghcr.io/product-science/proxy:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/proxy:latest; \
	fi

release:
	@echo "--> releasing proxy (multi-arch)"
	@echo "platforms: $(PLATFORMS)"
	$(eval DOCKER_FILE=Dockerfile)
	@docker buildx build \
		--platform $(PLATFORMS) \
		-f $(DOCKER_FILE) \
		-t ghcr.io/product-science/proxy:$(VERSION) \
		--push \
		.
