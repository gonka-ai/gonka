.PHONY: build clean help

PROJECT_NAME := liquidity-pool
ARTIFACTS_DIR := artifacts

# Default target
build: clean
	@echo "🔨 Building $(PROJECT_NAME) contract..."
	@mkdir -p $(ARTIFACTS_DIR)
	@docker run \
		-v "$(CURDIR)":/code \
		--mount type=volume,source="$(PROJECT_NAME)_cache",target=/code/target \
		--mount type=volume,source=registry_cache,target=/usr/local/cargo/registry \
		cosmwasm/rust-optimizer:0.17.0 > /dev/null 2>&1
	@echo "✅ Build complete: $(ARTIFACTS_DIR)/$(PROJECT_NAME).wasm"

clean:
	@echo "🧹 Cleaning build artifacts..."
	@rm -rf $(ARTIFACTS_DIR) target/

help:
	@echo "Available targets:"
	@echo "  build  - Build optimized WASM contract"
	@echo "  clean  - Clean build artifacts"
	@echo "  help   - Show this help message" 