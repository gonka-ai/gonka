.PHONY: build test e2e e2e-clean-results e2e-build e2e-run e2e-report testapp build-docker docker-push release

include ../scripts/blst-portable.mk

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
USE_REGISTRY_CACHE ?= 0
GHCR_CACHE_NAMESPACE ?= gonka-ai
ifeq ($(USE_REGISTRY_CACHE),1)
DOCKER_CACHE_ARGS := --cache-from type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/versiond:buildcache --cache-to type=registry,ref=ghcr.io/$(GHCR_CACHE_NAMESPACE)/versiond:buildcache,mode=min
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
	go build -ldflags "-X main.Version=testapp2 -X main.BinaryVersion=testapp2" -o build/testapp2 ./e2e/testapp
	go build -ldflags "-X main.Version=testapp -X main.BinaryVersion=testapp-rollout" -o build/testapp-rollout ./e2e/testapp

e2e: e2e-clean-results e2e-build e2e-run e2e-report

e2e-clean-results:
	rm -rf build/test-results
	mkdir -p build/test-results

e2e-build:
	docker compose -f e2e/docker-compose.yml build

e2e-run:
	docker compose -f e2e/docker-compose.yml up --no-build --abort-on-container-exit --exit-code-from tests
	docker compose -f e2e/docker-compose.yml down -v
	docker compose -f e2e/docker-compose.yml up --no-build -d oracle versiond
	docker compose -f e2e/docker-compose.yml stop versiond
	docker compose -f e2e/docker-compose.yml run --rm --no-deps -e REGISTER_STARTUP_VERSION=1 tests gotestsum --format testname --junitfile /app/test-results/reconcile-register-junit.xml -- -tags e2e ./e2e -v -timeout 300s -run '^TestRegisterStartupVersion$$'
	docker compose -f e2e/docker-compose.yml start versiond
	docker compose -f e2e/docker-compose.yml run --rm --no-deps -e EXPECT_INITIAL_TESTAPP=1 tests gotestsum --format testname --junitfile /app/test-results/reconcile-on-start-junit.xml -- -tags e2e ./e2e -v -timeout 300s -run '^TestStartupFromExistingOracleState$$'
	docker compose -f e2e/docker-compose.yml down -v

# e2e-report requires uv on PATH (install: pip install uv).
e2e-report:
	uvx junitparser merge --glob 'build/test-results/*-junit.xml' build/test-results/e2e-junit.xml
	@status=0; \
	uvx junitparser verify build/test-results/e2e-junit.xml || status=$$?; \
	uvx junit2html --summary-matrix build/test-results/e2e-junit.xml; \
	exit $$status

build-docker:
	$(eval PLATFORM=$(DOCKER_PLATFORM))
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
