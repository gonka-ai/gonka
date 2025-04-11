.PHONY: release decentralized-api-release inference-chain-release check-docker build-testermint run-blockchain-tests test-blockchain

VERSION ?= $(shell git describe --always)
TAG_NAME := "release/v$(VERSION)"

all: build-docker

build-docker: api-build-docker node-build-docker

api-build-docker:
	@make -C decentralized-api build-docker SET_LATEST=1

node-build-docker:
	@make -C inference-chain build-docker SET_LATEST=1

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
	@make -C inference-chain docker-push

launch-test-chain:
	./launch-local-test-chain.sh

stop-test-chain:
	./stop-test-local-chain.sh

check-docker:
	@docker info > /dev/null 2>&1 || (echo "Docker Desktop is not running. Please start Docker Desktop." && exit 1)

run-tests:
	@cd testermint && ./gradlew test --tests "*" -DexcludeTags=unstable,exclude

test-blockchain: check-docker run-blockchain-tests
