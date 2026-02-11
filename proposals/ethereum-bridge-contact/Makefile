.PHONY: build clean help install

PROJECT_NAME := ethereum-bridge-contract
ARTIFACTS_DIR := artifacts
CONTRACTS_DIR := contracts

# Cache directories (using bind mounts instead of Docker volumes)
CACHE_DIR := $(HOME)/.cache/ethereum-contracts
NODE_MODULES_CACHE := $(CACHE_DIR)/$(PROJECT_NAME)_node_modules

# Node.js version for reproducible builds
NODE_IMAGE := node:22-alpine

# Default target
build: install
	@echo "🔨 Compiling $(PROJECT_NAME) contract..."
	@mkdir -p $(ARTIFACTS_DIR)
	@docker run --rm \
		-v "$(CURDIR)":/workspace \
		-v "$(NODE_MODULES_CACHE)":/workspace/node_modules \
		-w /workspace \
		$(NODE_IMAGE) \
		npx hardhat compile
	@echo "✅ Build complete: $(ARTIFACTS_DIR)/contracts/BridgeContract.sol/BridgeContract.json"

install:
	@echo "📦 Installing dependencies..."
	@mkdir -p $(NODE_MODULES_CACHE)
	@docker run --rm \
		-v "$(CURDIR)":/workspace \
		-v "$(NODE_MODULES_CACHE)":/workspace/node_modules \
		-w /workspace \
		$(NODE_IMAGE) \
		npm ci --quiet
	@echo "✅ Dependencies installed"

clean:
	@echo "🧹 Cleaning build artifacts..."
	@rm -rf $(ARTIFACTS_DIR) cache/ typechain-types/
	@echo "✅ Clean complete"

clean-all: clean
	@echo "🧹 Cleaning all caches..."
	@rm -rf $(NODE_MODULES_CACHE)
	@echo "✅ All caches cleaned"

help:
	@echo "Available targets:"
	@echo "  build      - Compile smart contract in Docker (installs deps if needed)"
	@echo "  install    - Install npm dependencies in Docker"
	@echo "  clean      - Clean build artifacts"
	@echo "  clean-all  - Clean build artifacts and all caches"
	@echo "  help       - Show this help message"
	@echo ""
	@echo "Build configuration:"
	@echo "  Node.js:    $(NODE_IMAGE)"
	@echo "  Solidity:   0.8.19 (configured in hardhat.config.js)"
	@echo "  Optimizer:  enabled (200 runs)"

