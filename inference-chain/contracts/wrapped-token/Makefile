.PHONY: build clean help

PROJECT_NAME := wrapped-token
ARTIFACTS_DIR := artifacts

# Cache directories (using bind mounts instead of Docker volumes)
CACHE_DIR := $(HOME)/.cache/cosmwasm
TARGET_CACHE := $(CACHE_DIR)/$(PROJECT_NAME)_target
REGISTRY_CACHE := $(CACHE_DIR)/registry

# Default target
build: clean
	@echo "🔨 Building $(PROJECT_NAME) contract..."
	@mkdir -p $(ARTIFACTS_DIR) $(TARGET_CACHE) $(REGISTRY_CACHE)
	@docker run \
		-v "$(CURDIR)":/code \
		-v "$(TARGET_CACHE)":/code/target \
		-v "$(REGISTRY_CACHE)":/usr/local/cargo/registry \
		cosmwasm/rust-optimizer:0.17.0
	@echo "✅ Build complete: $(ARTIFACTS_DIR)/$(PROJECT_NAME).wasm"

clean:
	@echo "🧹 Cleaning build artifacts..."
	@rm -rf $(ARTIFACTS_DIR) target/

help:
	@echo "Available targets:"
	@echo "  build  - Build optimized WASM contract"
	@echo "  clean  - Clean build artifacts"
	@echo "  help   - Show this help message" 