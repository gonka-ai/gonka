.PHONY: all build-docker docker-push release test

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)

define DOCKER_BUILD
	@echo "--> building proxy-ssl docker image"
	@echo "    	platform: $(PLATFORM)"
	@docker build \
		--platform $(PLATFORM) \
		-f $(DOCKER_FILE) \
		. \
		-t $(DOCKER_TAG)
endef

all: build-docker

build-docker:
	$(eval PLATFORM=linux/amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=ghcr.io/product-science/proxy-ssl:$(VERSION))
	$(DOCKER_BUILD)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag $(DOCKER_TAG) ghcr.io/product-science/proxy-ssl:latest; \
	fi

docker-push:
	@echo "--> pushing proxy-ssl docker image"
	@echo "pushing to GitHub Container Registry"
	@docker push ghcr.io/product-science/proxy-ssl:$(VERSION)
	@if [ "$(SET_LATEST)" = "1" ]; then \
		echo "Setting latest tag..."; \
		docker tag ghcr.io/product-science/proxy-ssl:$(VERSION) ghcr.io/product-science/proxy-ssl:latest; \
		docker push ghcr.io/product-science/proxy-ssl:latest; \
	fi

test:
	@echo "--> running tests"
	@go test ./...

release: build-docker
