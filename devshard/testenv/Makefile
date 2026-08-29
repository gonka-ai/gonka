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

# devshard_testenv compiles in payload fault injection (DEVSHARD_TESTENV_PAYLOAD_*),
# which the production build omits. Never use these binaries for a release.
TESTENV_BUILD_TAGS := devshard_testenv

build-devshardd: ## Build linux devshardd for containers (requires Docker).
	$(MAKE) -C ../.. devshardd-build DEVSHARD_VERSION=v2 DEVSHARD_BINARY_VERSION=0.2.13-v2-r2 \
		DEVSHARD_BUILD_TAGS='$(TESTENV_BUILD_TAGS)'

build-devshardd-local: ## Build host devshardd for --print-* checks (no Docker).
	@mkdir -p ../../build
	cd .. && go build -tags '$(TESTENV_BUILD_TAGS)' -ldflags "-X main.Version=v2 -X main.BinaryVersion=0.2.13-v2-r2 -X devshard/types.buildStateRootProtocolVersion=v2" -o ../build/devshardd ./cmd/devshardd/
	@echo "Built ../../build/devshardd for this host ($(shell uname -s)/$(shell uname -m))"
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
.PHONY: citest-versiond-rolling-update citest-versiond-host-evacuation citest-adversarial citest-payload-withholding
.PHONY: citest-observability citest-grpc-transport citest-force-upstream-streaming citest-height-sync citest-height-sync-dapi-compat test-gateway-smoke

# citest-images and citest-stack-build are build/helper rules, not standalone
# suites, so they are excluded from the auto-discovered CI matrix.
list-citest-targets: ## Print runnable citest suite targets (one per line) for the CI matrix.
	@grep -hoE '^citest-[A-Za-z0-9_-]+:' $(firstword $(MAKEFILE_LIST)) \
		| sed 's/://' \
		| grep -vxE 'citest-images|citest-stack-build|citest-height-sync-dapi-compat' \
		| sort -u
.PHONY: obs-up obs-down
# versiond-0 produces devshard-versiond:latest shared by all versiond-* hosts.
CITEST_BUILD_SERVICES := mock-chain mock-dapi mock-openai devshardctl versiond-router versiond-0
STACK_CITEST_PATTERN := ^Test(StackSmoke|RouterStickiness|ParamsLongPoll|EpochSwitch|GatewayChat|Versiond.*|DynamicCatalogRemovalAndReadmission|HAStaleStandbyCatchupIdempotent|LegacyVersionPinnedToSingleHost|SQLiteToPostgresHAMigration|ValidationLeaseRace.*)$$

citest-images: gen-compose ## Pull hub images and build all compose images required for citest.
	docker compose $(COMPOSE_BASE) pull devshard-postgres
	docker compose $(COMPOSE_BASE) build $(CITEST_BUILD_SERVICES)

citest-stack: citest-images ## Full stack behavior citests (needs Docker + build-devshardd).
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '$(STACK_CITEST_PATTERN)' -count=1 -v -timeout 90m

citest-validation-lease-race: citest-images ## Validation lease race, pending stretch, and stale reclaim.
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^TestValidationLeaseRace' -count=1 -v -timeout 45m

citest-payload-withholding: citest-images ## Executor payload 500 → challenge/invalidate and D7-off lease release.
	$(MAKE) build-devshardd
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^TestPayloadWithholding' -count=1 -v -timeout 45m

citest-escrow-longpoll: citest-images ## Escrow long-poll warm; first inference without live escrow fetch (v4-deploy-test-plan §6).
	$(MAKE) build-devshardd
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^TestEscrowLongPollWarm' -count=1 -v -timeout 30m

citest-versiond-rolling-update: citest-images ## Same-version versiond rolling update and fallback.
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestVersiondRollingUpdate' -count=1 -v -timeout 30m

citest-versiond-host-evacuation: citest-images ## Router-controlled versiond host evacuation and replacement.
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^TestVersiondHostEvacuation$$' -count=1 -v -timeout 30m

citest-adversarial: citest-images ## Phase 9 adversarial citest (TESTENV_CITEST=1).
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestA1_|TestA2_|TestA3_|TestA4_|TestA5_' -count=1 -v -timeout 30m

citest-force-upstream-streaming: citest-images ## Force-upstream streaming + aggregate spill citest.
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestGatewayForcedStream|TestGatewayAggregateSpill|TestGatewayAggregateOversize' -count=1 -v -timeout 60m

citest-grpc-transport: citest-images ## Phase 12b G1–G4 gRPC-only gateway citest (see docs/chain-transport-consolidation.md).
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestG1_|TestG2_|TestG3_' -count=1 -v -timeout 30m
	cd .. && go test ./cmd/devshardctl/ -run TestG4_NoRESTChainClientsInGatewayProduction -count=1 -v

citest-observability: citest-images ## Phase 10 O1 observability smoke (Jaeger spans + Loki logs + metrics).
	$(MAKE) build-devshardd
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run TestO1_ObservabilitySmoke -count=1 -v -timeout 30m

citest-height-sync: citest-images ## Height-sync container e2e against mock-dapi chainoracle (no heightsyncd).
	$(MAKE) build-devshardd
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^(TestHeightSync_|TestContainerE2E_HeightSync_)' -count=1 -v -timeout 45m

# Local-only: 0.2.15-v5 (mock /block/*) vs 0.2.15 (omit /block/*). Real images
# ghcr.io/product-science/api:0.2.15-v5 and :0.2.15 cannot replace mock-dapi
# (mock-chain is not CometBFT). Excluded from list-citest-targets.
citest-height-sync-dapi-compat: citest-images ## Height-sync backward compat: new dapi /block/* vs old dapi 404.
	$(MAKE) build-devshardd
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^TestHeightSync_(MockDapiBlockLatest|CadenceEmitsAnchor|LegacyDapiChatCompletes)$$' -count=1 -v -timeout 45m

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
