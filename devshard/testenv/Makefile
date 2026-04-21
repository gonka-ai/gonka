# devshard testenv Makefile
#
# Phase 0 scaffold. Most targets currently print their phase number and
# exit. They will be filled in as each phase lands; see
# devshard/docs/testenv.md.

SHELL := /bin/bash

.PHONY: help
help:
	@awk 'BEGIN{FS=":.*##"; printf "Usage:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ---------------------------------------------------------------- build ----

.PHONY: proto
proto: ## Phase 2: generate gRPC stubs from proto/.
	@echo "TODO(phase-2): regenerate mock-chain gRPC stubs"

.PHONY: build
build: ## Phase 0+: compile all testenv binaries.
	go build ./...

# ------------------------------------------------------------- compose ----

.PHONY: gen-compose
gen-compose: ## Phase 10: render docker-compose.yml from config.yaml.
	@echo "TODO(phase-10): implement cmd/gencompose"

.PHONY: up
up: ## Phase 11: start the base stack.
	@echo "TODO(phase-11): docker compose up -d"

.PHONY: down
down: ## Phase 11: stop the base stack.
	@echo "TODO(phase-11): docker compose down -v"

.PHONY: logs
logs: ## Phase 11: tail logs from all services.
	@echo "TODO(phase-11): docker compose logs -f"

# ---------------------------------------------------------- dev overlay ---

.PHONY: dev-up
dev-up: ## Phase 12: start stack with live-reload + dlv overlay.
	@echo "TODO(phase-12): docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d"

.PHONY: dev-down
dev-down: ## Phase 12: stop dev overlay.
	@echo "TODO(phase-12): docker compose -f docker-compose.yml -f docker-compose.dev.yml down"

.PHONY: dev-rebuild
dev-rebuild: ## Phase 12: rebuild the dev toolchain image.
	@echo "TODO(phase-12): docker compose build --no-cache dev-toolchain"

# ---------------------------------------------------------- observability -

.PHONY: obs-up
obs-up: ## Phase 13: start observability stack (Alloy/Loki/Grafana/VM).
	@echo "TODO(phase-13): docker compose -f docker-compose.yml -f docker-compose.obs.yml up -d"

.PHONY: obs-down
obs-down: ## Phase 13: stop observability stack.
	@echo "TODO(phase-13): docker compose -f docker-compose.yml -f docker-compose.obs.yml down"

# --------------------------------------------------------------- tests ----

.PHONY: test
test: ## Phase 0+: run unit tests in the testenv module.
	go test ./...

.PHONY: integration-test
integration-test: ## Phase 14: docker-compose integration tests.
	@echo "TODO(phase-14): drive compose stack and run protocol assertions"
