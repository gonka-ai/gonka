.PHONY: all tests integration-tests unit-tests build

export PROJECT_ROOT := $(shell git rev-parse --show-toplevel)

ENV_FILES := $(wildcard $(PROJECT_ROOT)/.env ./.env)
INTEGRATION_TEST_DIRS = \
	/app/packages/api/tests/integration \
	/app/packages/pow/tests/integration \
	/app/packages/train/tests/integration

ifneq ($(ENV_FILES),)
include $(ENV_FILES)
export
endif

setup-envs:
	@echo "" > .env

build:
	docker compose build

integration-tests: build
	for test_dir in $(INTEGRATION_TEST_DIRS); do \
		docker compose down --remove-orphans; \
		docker compose run --rm integration-tests pytest -v $$test_dir; \
	done
	docker compose down --remove-orphans

unit-tests:
	docker compose build
	docker compose down --remove-orphans
	docker compose run --rm unit-tests pytest -v /app/packages/api/tests/unit
	docker compose down --remove-orphans

unit-tests-local:
	pytest -v tests/unit

unit-tests-gpu:

unit-tests-gpu-local:

generate-docs:
	@python $(PROJECT_ROOT)/packages/common/scripts/generate_docs.py -m "api.app:app" -o "docs" -t "API" -v "1.0.0" -d "API for MLNode"
