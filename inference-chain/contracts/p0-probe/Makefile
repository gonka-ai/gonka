.PHONY: build check clean help

CONTAINER_ENGINE ?= docker
OPTIMIZER_IMAGE := cosmwasm/optimizer:0.17.0@sha256:7e0b9229c1a4118d0c9a2af2e7f5d95a91f264c26a2ce5681c779926e74d7f85
OPTIMIZER_PLATFORM := linux/amd64

build:
	@CONTAINER_ENGINE="$(CONTAINER_ENGINE)" ./build.sh build

check:
	@./build.sh verify
	@$(CONTAINER_ENGINE) run --rm \
		--platform "$(OPTIMIZER_PLATFORM)" \
		--volume "$(CURDIR):/code" \
		--workdir /code \
		--env CARGO_TARGET_DIR=/tmp/p0-probe-target \
		--entrypoint cargo \
		"$(OPTIMIZER_IMAGE)" test --locked

clean:
	@rm -rf artifacts target

help:
	@echo "build  Rebuild the committed Wasm fixture with the pinned optimizer"
	@echo "check  Verify the exact checksum manifest and run locked Rust tests"
	@echo "clean  Remove local Rust build output and committed generated fixtures"
