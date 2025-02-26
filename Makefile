.PHONY: release decentralized-api-release inference-chain-release

VERSION ?= $(shell git describe --always)
SET_LATEST ?= $(shell if [ "$(VERSION)" = "1" ]; then echo 1; else echo 0; fi)
TAG_NAME := "release/v$(VERSION)"

all: build-docker

build-docker: api-build-docker node-build-docker

api-build-docker:
	@make -C decentralized-api build-docker

node-build-docker:
	@make -C inference-chain build-docker

# TODO 'build and push'
all-build-and-push-docker: api-build-and-push-docker node-build-and-push-docker

api-build-and-push-docker:
	@make -C decentralized-api build-and-push-docker

node-build-and-push-docker:
	@make -C inference-chain build-and-push-docker

release: decentralized-api-release inference-chain-release
	@git tag $(TAG_NAME)
	# @git push origin $(TAG_NAME)

decentralized-api-release:
	@echo "Releasing decentralized-api..."
	@make -C decentralized-api release

inference-chain-release:
	@echo "Releasing inference-chain..."
	@make -C inference-chain release

launch-test-chain:
	./launch-local-test-chain.sh

stop-test-chain:
	./stop-test-local-chain.sh

