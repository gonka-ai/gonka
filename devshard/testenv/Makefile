# devshard testenv Makefile — Phase 4

SHELL := /bin/bash
COMPOSE_BASE := -f docker-compose.yml
DEV_OVERLAY  := -f docker-compose.yml -f docker-compose.dev.yml

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN{FS=":.*##"; printf "Usage:\n"} /^[a-zA-Z0-9_.%-]+:.*##/ {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: gen-compose build-devshardd build-devshardd-local test-compose clean-keyring
gen-compose: ## Render config, keyrings, docker-compose.yml, and .env from config.yaml.
	go run ./cmd/gencompose -config config/config.yaml

clean-keyring: ## Delete generated ./keyring (then run make gen-compose).
	rm -rf keyring

DEVSHARD_VERSION ?= v2
DEVSHARD_BINARY_VERSION ?= 0.2.13-$(DEVSHARD_VERSION)-r2

build-devshardd: ## Build linux devshardd for containers (requires Docker). Override DEVSHARD_VERSION=v5 for reconnect citest.
	$(MAKE) -C ../.. devshardd-build DEVSHARD_VERSION=$(DEVSHARD_VERSION) DEVSHARD_BINARY_VERSION=$(DEVSHARD_BINARY_VERSION)

build-devshardd-local: ## Build host devshardd for --print-* checks (no Docker).
	@mkdir -p ../../build
	cd .. && go build -ldflags "-X main.Version=$(DEVSHARD_VERSION) -X main.BinaryVersion=$(DEVSHARD_BINARY_VERSION) -X devshard/types.buildStateRootProtocolVersion=$(DEVSHARD_VERSION)" -o ../build/devshardd ./cmd/devshardd/
	@echo "Built ../../build/devshardd for this host ($(shell uname -s)/$(shell uname -m)) version=$(DEVSHARD_VERSION)"
	@echo "For Docker stack use: make build-devshardd (requires Docker daemon)"

test-compose: ## gencompose + docker compose config validation.
	go test ./citest/... -count=1

.PHONY: build test
build: ## Compile testenv binaries locally.
	go build ./...

test: ## Run testenv unit tests.
	go test ./...

.PHONY: up down clean logs
up: ## Start the base stack.
	docker compose $(COMPOSE_BASE) up -d

down: ## Stop the base stack.
	docker compose $(COMPOSE_BASE) down

clean: ## Stop the stack and remove anonymous volumes.
	docker compose $(COMPOSE_BASE) down -v

logs: ## Follow service logs.
	docker compose $(COMPOSE_BASE) logs -f

.PHONY: dev-build dev-up hot-up dev-down dev-clean dev-logs dev-restart-mock-chain
dev-build: ## Build the shared dev-toolchain image.
	docker compose $(DEV_OVERLAY) build

dev-up hot-up: ## Start stack with live-reload overlay.
	docker compose $(DEV_OVERLAY) up -d

dev-down: ## Stop dev overlay stack.
	docker compose $(DEV_OVERLAY) down

dev-clean: ## Stop dev stack and remove volumes.
	docker compose $(DEV_OVERLAY) down -v

dev-logs: ## Follow dev overlay logs.
	docker compose $(DEV_OVERLAY) logs -f

.PHONY: citest-images citest-stack citest-stack-build list-citest-targets
.PHONY: citest-validation-lease-race citest-escrow-longpoll
.PHONY: citest-versiond-rolling-update citest-adversarial
.PHONY: citest-observability citest-grpc-transport citest-attempt-reconnect test-gateway-smoke

# citest-images and citest-stack-build are build/helper rules, not standalone
# suites, so they are excluded from the auto-discovered CI matrix.
list-citest-targets: ## Print runnable citest suite targets (one per line) for the CI matrix.
	@grep -hoE '^citest-[A-Za-z0-9_-]+:' $(firstword $(MAKEFILE_LIST)) \
		| sed 's/://' \
		| grep -vxE 'citest-images|citest-stack-build' \
		| sort -u
.PHONY: obs-up obs-down
# versiond-0 produces devshard-versiond:latest shared by all versiond-* hosts.
# Root docker-compose.yml names mock-openai as mock-openai-0 (per-host); citest
# gencompose stacks reuse the same image tags (devshard-mock-openai:latest, …).
CITEST_BUILD_SERVICES := mock-chain mock-dapi mock-openai-0 devshardctl versiond-router versiond-0
STACK_CITEST_PATTERN := ^Test(StackSmoke|RouterStickiness|ParamsLongPoll|EpochSwitch|GatewayChat|Versiond.*|HAStaleStandbyCatchupIdempotent|LegacyVersionPinnedToSingleHost|SQLiteToPostgresHAMigration|ValidationLeaseRace.*)$$

citest-images: ## Pull hub images and build all compose images required for citest.
	docker compose $(COMPOSE_BASE) pull devshard-postgres
	docker compose $(COMPOSE_BASE) build $(CITEST_BUILD_SERVICES)

citest-stack: citest-images ## Full stack behavior citests (needs Docker + build-devshardd).
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '$(STACK_CITEST_PATTERN)' -count=1 -v -timeout 90m

citest-validation-lease-race: citest-images ## Validation lease race, pending stretch, and stale reclaim.
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^TestValidationLeaseRace' -count=1 -v -timeout 45m

citest-escrow-longpoll: citest-images ## Escrow long-poll warm; first inference without live escrow fetch (v4-deploy-test-plan §6).
	$(MAKE) build-devshardd
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^TestEscrowLongPollWarm' -count=1 -v -timeout 30m

citest-versiond-rolling-update: citest-images ## Same-version versiond rolling update and fallback.
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestVersiondRollingUpdate' -count=1 -v -timeout 30m

citest-adversarial: citest-images ## Phase 9 adversarial citest (TESTENV_CITEST=1).
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestA1_|TestA2_|TestA3_|TestA4_' -count=1 -v -timeout 30m

citest-attempt-reconnect: ## Same-nonce reconnect citest (admin + v2 protocol gate, then v5 resume).
	$(MAKE) build-devshardd DEVSHARD_VERSION=v2
	$(MAKE) citest-images
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestAttemptReconnect_AdminEnables|TestAttemptReconnect_V2ProtocolSkipsSameNonce' -count=1 -v -timeout 30m
	$(MAKE) build-devshardd DEVSHARD_VERSION=v5
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestAttemptReconnect_V5MidStreamDetachResumesSameNonce' -count=1 -v -timeout 30m
	$(MAKE) build-devshardd DEVSHARD_VERSION=v2

citest-grpc-transport: citest-images ## Phase 12b G1–G4 gRPC-only gateway citest (see docs/chain-transport-consolidation.md).
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestG1_|TestG2_|TestG3_' -count=1 -v -timeout 30m
	cd .. && go test ./cmd/devshardctl/ -run TestG4_NoRESTChainClientsInGatewayProduction -count=1 -v

citest-observability: citest-images ## Phase 10 O1 observability smoke (Jaeger spans + Loki logs + metrics).
	$(MAKE) build-devshardd
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run TestO1_ObservabilitySmoke -count=1 -v -timeout 30m

OBS_OVERLAY := $(COMPOSE_BASE) -f docker-compose.observability.yml -f docker-compose.observability.ip.yml

obs-up: ## Start base stack + observability overlay (Jaeger/Loki/Prometheus/Grafana).
	TESTENV_OTEL_ENABLED=true TESTENV_OTEL_ENDPOINT=http://jaeger:4317 \
	  docker compose $(COMPOSE_BASE) up -d --wait
	TESTENV_OTEL_ENABLED=true TESTENV_OTEL_ENDPOINT=http://jaeger:4317 \
	  docker compose $(OBS_OVERLAY) up -d

obs-down: ## Stop observability overlay (and base stack if merged).
	docker compose $(OBS_OVERLAY) down

citest-stack-build: dev-build ## Build dev images then run full stack citests.
	$(MAKE) citest-stack

test-gateway-smoke: ## Full-stack gateway smoke (TESTENV_GATEWAY_SMOKE=1; needs Docker + build-devshardd).
	TESTENV_GATEWAY_SMOKE=1 go test ./citest/ -run TestGatewayPhase7_Smoke -count=1 -v

dev-restart-mock-chain: ## Restart mock-chain under dev overlay.
	docker compose $(DEV_OVERLAY) restart mock-chain
