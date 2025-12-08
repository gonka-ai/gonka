.PHONY: all tests integration-tests unit-tests build start-server

export PROJECT_ROOT := $(shell git rev-parse --show-toplevel)/mlnode

ENV_FILES := $(wildcard $(PROJECT_ROOT)/.env ./.env)
INTEGRATION_TEST_DIRS = \
	/app/packages/validator/tests/integration

ifneq ($(ENV_FILES),)
include $(ENV_FILES)
export
endif

setup-envs:
	@echo "" > .env

build:
	docker compose build

start-server:
	docker compose up -d server

integration-tests: build
	for test_dir in $(INTEGRATION_TEST_DIRS); do \
		docker compose down --remove-orphans; \
		docker compose run --rm integration-tests /app/packages/validator/.venv/bin/pytest -v $$test_dir; \
	done
	docker compose down --remove-orphans

unit-tests:
	docker compose build
	docker compose down --remove-orphans
	docker compose run --rm unit-tests /app/packages/validator/.venv/bin/pytest -v /app/packages/validator/tests/unit
	docker compose down --remove-orphans

unit-tests-local:
	pytest -v tests/unit

unit-tests-gpu:

unit-tests-gpu-local:

generate-docs:
	@python $(PROJECT_ROOT)/packages/common/scripts/generate_docs.py -m "validator.app:app" -o "docs" -t "Validator" -v "0.1.0" -d "Validator for MLNode"
