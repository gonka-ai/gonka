# Subnet testenv Makefile
# Run from: subnet/testenv/
# Docker images are built from the repository root (../../..) so the `replace`
# directive in go.mod resolves correctly.

REPO_ROOT   := $(shell cd ../.. && pwd)
CONFIG      ?= config.yaml
COMPOSE     ?= docker-compose.yml
DEV_COMPOSE ?= docker-compose.dev.yml
PROTOC_OUT  := mockchain
BIN         := bin

.PHONY: all gen-compose proto build-local build build-subnetctl up down logs \
        run-subnetctl clean tidy dev-build dev-up dev-down dev-logs

all: gen-compose build

## gen-compose: generate private keys, assign slots, and write docker-compose.yml
gen-compose:
	@echo "→ Generating $(COMPOSE) from $(CONFIG)..."
	go run ./cmd/gencompose -config $(CONFIG) -out $(COMPOSE)
	@echo "✓ Done. Review $(COMPOSE) then run: make build && make up"

## build-local: compile binaries locally into bin/ (for local testing without Docker)
build-local:
	@mkdir -p $(BIN)
	go build -o $(BIN)/mockserver  ./cmd/mockserver
	go build -o $(BIN)/subnethost  ./cmd/subnethost
	go build -o $(BIN)/gencompose  ./cmd/gencompose
	go build -o $(BIN)/subnetctl   ./cmd/subnetctl
	@echo "✓ Binaries in $(BIN)/: mockserver  subnethost  gencompose  subnetctl"

## proto: regenerate Go gRPC stubs from proto/mockchain.proto
proto:
	@echo "→ Regenerating gRPC stubs from proto/mockchain.proto..."
	@mkdir -p $(PROTOC_OUT)
	protoc \
		--proto_path=proto \
		--go_out=$(PROTOC_OUT) \
		--go_opt=paths=source_relative \
		--go-grpc_out=$(PROTOC_OUT) \
		--go-grpc_opt=paths=source_relative \
		proto/mockchain.proto
	@echo "✓ Done. Files updated in $(PROTOC_OUT)/"

## tidy: download and tidy Go module dependencies
tidy:
	go mod tidy

## build: build all Docker images (run from repo root for context)
build:
	@echo "→ Building Docker images from $(REPO_ROOT)..."
	docker build \
		-f $(CURDIR)/Dockerfile.mockserver \
		-t subnet-mockserver:latest \
		$(REPO_ROOT)
	docker build \
		-f $(CURDIR)/Dockerfile.subnethost \
		-t subnet-host:latest \
		$(REPO_ROOT)
	docker build \
		-f $(CURDIR)/Dockerfile.subnetctl \
		-t subnet-subnetctl:latest \
		$(REPO_ROOT)
	@echo "✓ Images built: subnet-mockserver:latest  subnet-host:latest  subnet-subnetctl:latest"

## build-subnetctl: build only the subnetctl Docker image
build-subnetctl:
	@echo "→ Building subnetctl image from $(REPO_ROOT)..."
	docker build \
		-f $(CURDIR)/Dockerfile.subnetctl \
		-t subnet-subnetctl:latest \
		$(REPO_ROOT)
	@echo "✓ Image built: subnet-subnetctl:latest"

## up: start all containers in the background
up:
	@echo "→ Starting testenv..."
	docker compose -f $(COMPOSE) up -d
	@echo "✓ Up. Mock server health: http://localhost:9091/health"
	@echo "  subnetctl proxy: http://localhost:8081 (default; see config user.port)"
	@echo "  Use 'make logs' to follow output, 'make run-subnetctl' for subnetctl logs only."

## run-subnetctl: follow subnetctl logs (subnetctl starts with make up)
run-subnetctl:
	docker compose -f $(COMPOSE) logs -f subnetctl

## down: stop and remove all containers
down:
	docker compose -f $(COMPOSE) down

## logs: follow container logs
logs:
	docker compose -f $(COMPOSE) logs -f

## clean: remove generated files, local binaries, and Docker images
clean:
	rm -f $(COMPOSE)
	rm -rf $(BIN)
	docker rmi -f subnet-mockserver:latest subnet-host:latest subnet-subnetctl:latest 2>/dev/null || true

## dev-build: build the dev image (Go+air+delve); run once or after go.mod changes
dev-build:
	@echo "→ Building dev image from $(REPO_ROOT)..."
	docker build \
		-f $(CURDIR)/Dockerfile.dev \
		-t subnet-mockserver:dev \
		$(REPO_ROOT)
	docker tag subnet-mockserver:dev subnet-host:dev
	@echo "✓ Dev images ready: subnet-mockserver:dev  subnet-host:dev"

## dev-up: start testenv with live-reload and delve debug ports (2345=mock, 2346=p0)
dev-up:
	@echo "→ Starting dev testenv (live-reload + debug ports)..."
	docker compose -f $(COMPOSE) -f $(DEV_COMPOSE) up -d
	@echo "✓ Up. Debug ports: mock-server→:2345, participant-0→:2346"
	@echo "  Connect in VSCode/Cursor via .vscode/launch.json"
	@echo "  Use 'make dev-logs' to follow output."

## dev-down: stop dev testenv
dev-down:
	docker compose -f $(COMPOSE) -f $(DEV_COMPOSE) down

## dev-logs: follow dev container logs
dev-logs:
	docker compose -f $(COMPOSE) -f $(DEV_COMPOSE) logs -f

## help: print available targets
help:
	@grep -E '^##' Makefile | sed 's/## //'
