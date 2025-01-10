all: build-docker

build-docker: api-build-docker node-build-docker

api-build-docker:
	@make -C decentralized-api build-docker

node-build-docker:
	@make -C inference-chain build-docker

all-build-and-push-docker: api-build-and-push-docker node-build-and-push-docker

api-build-and-push-docker:
	@make -C decentralized-api build-and-push-docker

node-build-and-push-docker:
	@make -C inference-chain build-and-push-docker
