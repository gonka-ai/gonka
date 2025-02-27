.PHONY: release decentralized-api-release inference-chain-release

VERSION ?= $(shell git describe --always)
TAG_NAME := "release/v$(VERSION)"

all: build-docker

build-docker: api-build-docker node-build-docker

api-build-docker:
	@make -C decentralized-api build-docker

node-build-docker:
	@make -C inference-chain build-docker

release: decentralized-api-release inference-chain-release
	@git tag $(TAG_NAME)
	@git push origin $(TAG_NAME)

decentralized-api-release:
	@echo "Releasing decentralized-api..."
	@make -C decentralized-api release
	@make -C decentralized-api docker-push

inference-chain-release:
	@echo "Releasing inference-chain..."
	@make -C inference-chain release
	@make -C decentralized-api docker-push

launch-test-chain:
	./launch-local-test-chain.sh

stop-test-chain:
	./stop-test-local-chain.sh

