.PHONY: all

setup-envs:
	@echo "" > .env

build:
	docker compose build

integration-tests:
	docker compose build
	docker compose down --remove-orphans
	docker compose run --rm integration-tests pytest -v /app/packages/train/tests/integration
	docker compose down --remove-orphans

unit-tests:
	docker compose build
	docker compose down --remove-orphans
	docker compose run --rm unit-tests pytest -v /app/packages/train/tests/unit
	docker compose down --remove-orphans

unit-tests-local:
	pytest -v tests/unit

unit-tests-gpu:
	docker compose build
	docker compose down --remove-orphans
	docker compose run --rm unit-tests-gpu pytest -v /app/packages/train/tests/unit-gpu
	docker compose down --remove-orphans

unit-tests-gpu-local:
	pytest -v tests/unit-gpu

generate-docs:
	@python $(PROJECT_ROOT)/packages/common/scripts/generate_docs.py -m "zeroband.service.app:app" -o "docs" -t "Train API" -v "1.0.0" -d "API for Train"


