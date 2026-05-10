# devshard testenv Makefile
#
# Thin wrapper over `docker compose`, `go`, and `gencompose`. Every
# target that compose builds re-uses the default docker-compose.yml
# emitted by `make gen-compose`; the dev overlay lives in
# docker-compose.dev.yml and is only stacked under the `dev-*` targets.
#
# See devshard/docs/testenv.md §12 (dev overlay), §13 (observability), §14 (this file + docs).

SHELL := /bin/bash

# Canonical compose file pair. COMPOSE_BASE is the production-shape
# stack; DEV_OVERLAY layers live-reload + dlv on top of it.
COMPOSE_BASE    := -f docker-compose.yml
DEV_OVERLAY     := -f docker-compose.yml -f docker-compose.dev.yml

# Number of devshardd-testenv hosts the default config spawns.
# Makefile target dev-logs-% uses this to clamp 0..N-1.
DEVSHARDD_HOSTS := 4

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN{FS=":.*##"; printf "Usage:\n"} /^[a-zA-Z0-9_.%-]+:.*##/ {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ---------------------------------------------------------------- build ----

.PHONY: proto
proto: ## Regenerate mock-chain gRPC stubs (protoc + protoc-gen-go + protoc-gen-go-grpc on PATH).
	cd .. && PATH="$$HOME/go/bin:$$PATH" protoc -I testenv/proto testenv/proto/mockchain.proto \
		--go_out=. --go_opt=module=devshard \
		--go-grpc_out=. --go-grpc_opt=module=devshard

.PHONY: build
build: ## Compile every testenv binary locally (sanity check, not used in containers).
	go build ./...

.PHONY: test
test: ## Run the devshard + testenv unit tests.
	go test ./...

.PHONY: e2e
e2e: ## Phase A container scenarios (Docker + observability); skip with TESTENV_SKIP_DOCKER_STACK=1. Test logs include copy-paste `docker compose logs` lines for devshardctl / devshardd hosts.
	cd .. && go test -tags=testenvci -v -timeout=20m ./testenv/scenarios/container/...

# ------------------------------------------------------------- compose ----

.PHONY: gen-compose
gen-compose: ## Phase 10: render docker-compose.yml from config.yaml.
	go run ./cmd/gencompose

.PHONY: up
up: ## Phase 11: start the base stack in the background.
	docker compose $(COMPOSE_BASE) up -d

.PHONY: down
down: ## Phase 11: stop the base stack (keeps volumes).
	docker compose $(COMPOSE_BASE) down

.PHONY: clean
clean: ## Phase 11: stop the stack and wipe anonymous volumes.
	docker compose $(COMPOSE_BASE) down -v

.PHONY: logs
logs: ## Phase 11: follow logs from every service.
	docker compose $(COMPOSE_BASE) logs -f

.PHONY: ctl
ctl: ## Phase 11: run a one-off devshardctl CLI in the stack (ephemeral `run --rm`).
	docker compose $(COMPOSE_BASE) run --rm devshardctl

# ---------------------------------------------------------- dev overlay ---

.PHONY: dev-build
dev-build: ## Phase 12: (re)build the shared dev-toolchain image.
	docker compose $(DEV_OVERLAY) build

.PHONY: dev-up
dev-up: ## Phase 12: start stack with live-reload + dlv overlay.
	docker compose $(DEV_OVERLAY) up -d

.PHONY: hot-up
hot-up: dev-up ## Alias: start stack with hot reload (live-reload + dlv).

.PHONY: hot-recreate
hot-recreate: ## Recreate hot-reload stack containers (pick up .air/config changes).
	docker compose $(DEV_OVERLAY) up -d --force-recreate

.PHONY: dev-down
dev-down: ## Phase 12: stop dev overlay (keeps caches).
	docker compose $(DEV_OVERLAY) down

.PHONY: dev-clean
dev-clean: ## Phase 12: stop dev overlay and drop the module/build caches.
	docker compose $(DEV_OVERLAY) down -v

.PHONY: dev-logs
dev-logs: ## Phase 12: follow logs from every dev-mode service.
	docker compose $(DEV_OVERLAY) logs -f

# dev-logs-% — follow one devshardd-testenv-<k> only (e.g. `make dev-logs-0`).
# Useful when chasing air rebuild output on a single host.
.PHONY: dev-logs-%
dev-logs-%: ## Phase 12: follow logs from devshardd-testenv-<k> (k=0..N-1).
	@if [ "$*" -ge "$(DEVSHARDD_HOSTS)" ]; then \
		echo "host index $* is out of range (max $$(($(DEVSHARDD_HOSTS)-1)))"; exit 1; \
	fi
	docker compose $(DEV_OVERLAY) logs -f devshardd-testenv-$*

.PHONY: dev-restart-%
dev-restart-%: ## Phase 12: restart a single service inside the dev overlay.
	docker compose $(DEV_OVERLAY) restart $*

# ---------------------------------------------------------- observability -

# Observability is merged into docker-compose.yml; see testenv/observability/.

.PHONY: obs-up
obs-up: up ## Start full testenv (includes VictoriaMetrics, Alloy, Loki, cAdvisor, Grafana). Alias for 'up'.

.PHONY: obs-up-linux
obs-up-linux: up ## Same as up (node-exporter is in the default stack; kept for old muscle memory / scripts).

.PHONY: obs-down
obs-down: ## Stop only observability-related services (app stack may keep running).
	-docker compose $(COMPOSE_BASE) stop victoria-metrics alloy loki grafana cadvisor node-exporter

.PHONY: obs-logs
obs-logs: ## Follow core observability container logs.
	docker compose $(COMPOSE_BASE) logs -f victoria-metrics alloy grafana loki

.PHONY: obs-grafana-open
obs-grafana-open: ## Open Grafana (http://localhost:3000) if 'open' exists (macOS); else print URL.
	@command -v open >/dev/null 2>&1 && open "http://localhost:3000" || echo "http://localhost:3000"

.PHONY: obs-query-open
obs-query-open: ## Open VictoriaMetrics VMUI (http://127.0.0.1:8428/vmui) if 'open' exists; else print URL.
	@command -v open >/dev/null 2>&1 && open "http://127.0.0.1:8428/vmui" || echo "http://127.0.0.1:8428/vmui"

.PHONY: obs-reset
obs-reset: ## Recreate obs containers; does not remove named volumes (see OBSERVABILITY.md to wipe data).
	-docker compose $(COMPOSE_BASE) up -d --force-recreate victoria-metrics loki grafana cadvisor alloy
	@echo "To drop metrics/log data: remove Docker volumes obs-victoria, obs-loki, obs-grafana, obs-alloy (with compose project prefix)."

# --------------------------------------------------------------- tests ----

.PHONY: integration-test
integration-test: ## Run the full devshard module test suite (race, 15m cap). Can take several minutes — not stuck.
	@echo "devshard: go test -race ./... (timeout 15m) …"
	cd .. && go test -count=1 -race -timeout=15m ./...
