all: build-docker

build-docker: api-build-docker node-build-docker

release-docker: api-release-docker node-release-docker

api-build-docker:
	@make -C decentralized-api build-docker

node-build-docker:
	@make -C inference-chain build-docker

node-build-genesis:
	@make -C inference-chain build-genesis

api-release-docker:
	@make -C decentralized-api release-docker

node-release-docker:
	@make -C inference-chain release-docker

all-build-docker: api-build-docker node-build-docker

compose-up:
	@docker compose \
         --file docker-compose.yml \
         --project-name inference-chain up \
         --detach

compose-down:
	@docker compose \
         --project-name inference-chain down

sim-up:
	@docker compose -f docker-compose-sim.yml up

sim-down:
	@docker compose -f docker-compose-sim.yml down

all-build-and-push-docker: api-build-and-push-docker node-build-and-push-docker

api-build-and-push-docker:
	@make -C decentralized-api build-and-push-docker

node-build-and-push-docker:
	@make -C inference-chain build-and-push-docker
