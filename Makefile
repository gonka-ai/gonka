.PHONY: release decentralized-api-release inference-chain-release check-docker build-testermint run-blockchain-tests test-blockchain

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

check-docker:
	@docker info > /dev/null 2>&1 || (echo "Docker Desktop is not running. Please start Docker Desktop." && exit 1)

run-tests:
	@cd testermint && ./gradlew test --tests "*" -DexcludeTags=unstable,exclude

test-blockchain: check-docker run-blockchain-tests
