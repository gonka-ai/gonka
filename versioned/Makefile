.PHONY: build test e2e testapp build-docker docker-push release

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
USE_REGISTRY_CACHE ?= 0
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/gonka-ai/versiond:buildcache --cache-to type=registry,ref=ghcr.io/gonka-ai/versiond:buildcache,mode=min
DOCKER_BUILD_PREFIX := docker buildx build --load $(DOCKER_CACHE_ARGS)
else
DOCKER_CACHE_ARGS :=
DOCKER_BUILD_PREFIX := DOCKER_BUILDKIT=1 docker build
endif

define DOCKER_BUILD
	@echo "--> building versiond docker image"
	@echo "    platform: $(PLATFORM)"
	@$(DOCKER_BUILD_PREFIX) \
		--platform $(PLATFORM) \
		-f $(DOCKER_FILE) \
		. \
		-t $(DOCKER_TAG)
endef

build:
	mkdir -p build
	go build -o build/versiond ./cmd/versiond

test:
	go test ./... -v

testapp:
	mkdir -p build
	go build -o build/testapp ./e2e/testapp

e2e: build testapp
	docker compose -f e2e/docker-compose.yml up --build --abort-on-container-exit --exit-code-from tests

build-docker:
	$(eval PLATFORM=linux/amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=ghcr.io/product-science/versiond:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/versiond:latest; \
	fi

docker-push:
	@echo "--> pushing versiond docker image"
	@echo "pushing to GitHub Container Registry"
	@docker push ghcr.io/product-science/versiond:$(VERSION)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag ghcr.io/product-science/versiond:$(VERSION) ghcr.io/product-science/versiond:latest; \
		docker push ghcr.io/product-science/versiond:latest; \
	fi

release: build-docker
