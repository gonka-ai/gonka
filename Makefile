all: build-docker

build-docker: api-build-docker node-build-docker

api-build-docker:
	@make -C decentralized-api build-docker

node-build-docker:
	@make -C inference-chain build-docker

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
