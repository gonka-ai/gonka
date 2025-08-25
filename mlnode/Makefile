.PHONY: all tests integration-tests unit-tests setup-envs locks

MAKEFLAGS += --no-print-directory

export PROJECT_ROOT := $(shell git rev-parse --show-toplevel)

VERSION ?= $(shell git describe --always)
IMAGE_NAME := ghcr.io/gonka-ai/mlnode:$(VERSION)
IMAGE_NAME_GITHUB := ghcr.io/product-science/mlnode:$(VERSION)
LATEST_IMAGE_NAME := ghcr.io/gonka-ai/mlnode:latest
LATEST_IMAGE_NAME_GITHUB := ghcr.io/product-science/mlnode:latest
TAG_NAME := "release/v$(VERSION)"

ENV_FILES := $(wildcard $(PROJECT_ROOT)/.env ./.env)
ifneq ($(ENV_FILES),)
include $(ENV_FILES)
export
endif

down-all:
	docker compose -f $(PROJECT_ROOT)/packages/api/docker-compose.yml down
	docker compose -f $(PROJECT_ROOT)/packages/pow/docker-compose.yml down
	docker compose -f $(PROJECT_ROOT)/packages/train/docker-compose.yml down

setup-envs:
	@$(MAKE) -C packages/api setup-envs
	@$(MAKE) -C packages/pow setup-envs
	@$(MAKE) -C packages/train setup-envs

	@echo "PROJECT_ROOT=$(PROJECT_ROOT)" >.env
	@echo "CACHE_DIR=<CACHE_DIR>" >> .env
	@echo "HF_TOKEN=<HF_TOKEN>" >> .env 
	@echo "WANDB_API_KEY=<WANDB_API_KEY>" >> .env 
	@echo "WANDB_ENTITY=<WANDB_ENTITY>" >> .env 
	@echo "ZERO_BAND_LOG_LEVEL=DEBUG" >> .env 
	@echo "ZERO_BAND_LOG_ALL_RANK=true" >> .env 

	@echo "Environment are set in .env."
	@echo "Please fill in the values. And load the env with 'source .env'" 

setup-dev-env:
	POETRY_VIRTUALENVS_IN_PROJECT=true \
		POETRY_VIRTUALENVS_CREATE=true \
		poetry install

locks:
	cd packages/common \
		&& rm -rf poetry.lock && POETRY_VIRTUALENVS_CREATE=false poetry lock
	cd packages/pow \
		&& rm -rf poetry.lock && POETRY_VIRTUALENVS_CREATE=false poetry lock
	cd packages/train \
		&& rm -rf poetry.lock && POETRY_VIRTUALENVS_CREATE=false poetry lock
	cd packages/api \
		&& rm -rf poetry.lock && POETRY_VIRTUALENVS_CREATE=false poetry lock
	cd packages/benchmarks \
		&& rm -rf poetry.lock && POETRY_VIRTUALENVS_CREATE=false poetry lock
	rm -rf poetry.lock && \
		POETRY_VIRTUALENVS_IN_PROJECT=true \
		POETRY_VIRTUALENVS_CREATE=true \
		poetry lock

integration-tests: down-all
	@echo "Running integration tests"
	@$(MAKE) -C packages/api integration-tests
	@$(MAKE) -C packages/pow integration-tests
	@$(MAKE) -C packages/train integration-tests

unit-tests:
	@echo "Running unit tests"
	@$(MAKE) -C packages/api unit-tests
	@$(MAKE) -C packages/pow unit-tests
	@$(MAKE) -C packages/train unit-tests

unit-tests-local:
	@$(MAKE) -C packages/api unit-tests-local
	@$(MAKE) -C packages/pow unit-tests-local
	@$(MAKE) -C packages/train unit-tests-local

unit-tests-gpu: down-all
	@echo "Running GPU unit tests"
	@$(MAKE) -C packages/api unit-tests-gpu
	@$(MAKE) -C packages/pow unit-tests-gpu
	@$(MAKE) -C packages/train unit-tests-gpu

unit-tests-gpu-local:
	@$(MAKE) -C packages/api unit-tests-gpu-local
	@$(MAKE) -C packages/pow unit-tests-gpu-local
	@$(MAKE) -C packages/train unit-tests-gpu-local

tests: unit-tests integration-tests unit-tests-gpu


build-release:
	docker build \
		-t $(IMAGE_NAME) \
		-f packages/api/Dockerfile \
		--target app \
		$(PROJECT_ROOT)

	docker tag $(IMAGE_NAME) $(IMAGE_NAME_GITHUB)

push-release: build-release
	@echo "Pushing $(IMAGE_NAME)"
	docker push $(IMAGE_NAME)
	@echo "Pushing $(IMAGE_NAME_GITHUB)"
	docker push $(IMAGE_NAME_GITHUB)

push-latest:
	@echo "Tagging $(IMAGE_NAME) as $(LATEST_IMAGE_NAME)"
	docker tag $(IMAGE_NAME) $(LATEST_IMAGE_NAME)
	@echo "Pushing $(LATEST_IMAGE_NAME)"
	docker push $(LATEST_IMAGE_NAME)

	@echo "Tagging $(IMAGE_NAME_GITHUB) as $(LATEST_IMAGE_NAME_GITHUB)"
	docker tag $(IMAGE_NAME_GITHUB) $(LATEST_IMAGE_NAME_GITHUB)
	@echo "Pushing $(LATEST_IMAGE_NAME_GITHUB)"
	docker push $(LATEST_IMAGE_NAME_GITHUB)

release: tests build-release push-release
	git tag $(TAG_NAME)
	git push origin $(TAG_NAME)

release-no-tests: build-release push-release
	git tag $(TAG_NAME)
	git push origin $(TAG_NAME)

release-latest: release push-latest


generate-docs:
	@$(MAKE) -C packages/api generate-docs
	@$(MAKE) -C packages/pow generate-docs
	@$(MAKE) -C packages/train generate-docs
