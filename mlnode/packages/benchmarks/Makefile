.PHONY: all

export PROJECT_ROOT := $(shell git rev-parse --show-toplevel)

ENV_FILES := $(wildcard $(PROJECT_ROOT)/.env ./.env)
ifneq ($(ENV_FILES),)
include $(ENV_FILES)
export
endif

setup-envs:
	@echo "" > .env

build:
	@echo "No build action defined"

integration-tests:
	@echo "No integration tests defined"

unit-tests:
	@echo "No unit tests defined"

unit-tests-local:
	pytest -v tests/unit

unit-tests-gpu:
	@echo "No unit tests GPU defined"

unit-tests-gpu-local:
	@echo "No unit tests GPU local defined"


generate-docs:
	@echo "No generate docs defined"
