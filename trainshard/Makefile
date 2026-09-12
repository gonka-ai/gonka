.PHONY: test build image push

VERSION ?= $(shell git describe --always)
IMAGE ?= ghcr.io/gonka-ai/trainshardd
PLATFORM ?= linux/amd64

test:
	go test ./...

build:
	go build -o build/trainshardd ./cmd/trainshardd
	go build -o build/trainshardctl ./cmd/trainshardctl

image:
	DOCKER_BUILDKIT=1 docker build \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		-f Dockerfile \
		..

push:
	docker buildx build \
		--platform $(PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) \
		--push \
		-f Dockerfile \
		..
