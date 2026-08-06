.PHONY: build test clean help

PROJECT_NAME := reward_splitter
ARTIFACTS_DIR := artifacts

build:
	@./build.sh

test:
	@cargo test

clean:
	@echo "🧹 Cleaning build artifacts..."
	@rm -rf $(ARTIFACTS_DIR) target/

help:
	@echo "Available targets:"
	@echo "  build  - Build the optimized WASM contract"
	@echo "  test   - Run contract unit tests"
	@echo "  clean  - Clean build artifacts"
	@echo "  help   - Show this help message"
