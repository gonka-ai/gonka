.PHONY: all build-docker

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0

all: build-docker

build-docker:
	DOCKER_BUILDKIT=1 docker build \
		--platform linux/amd64 \
		-f Dockerfile \
		. \
		-t ghcr.io/product-science/edge-api-router:$(VERSION)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag ghcr.io/product-science/edge-api-router:$(VERSION) ghcr.io/product-science/edge-api-router:latest; \
		docker tag ghcr.io/product-science/edge-api-router:$(VERSION) edge-api-router:latest; \
	fi
