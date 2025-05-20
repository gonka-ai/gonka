.PHONY: release build-with-keygen build-with-import release

PLATFORM ?= linux/amd64

VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)

build-with-keygen:
	docker build \
		--platform $(PLATFORM) \
		-f ./docker/Dockerfile_keygen \
		--build-arg TMKMS_TOML=./template/tmkms.toml \
		-t ghcr.io/product-science/tmkms-softsign-with-keygen:$(VERSION) \
		.

build-with-import:
	docker build \
		--platform $(PLATFORM) \
		-f ./docker/Dockerfile_import_keys \
		--build-arg TMKMS_TOML=./template/tmkms.toml \
		--build-arg PRIV_VALIDATOR_KEY=priv_validator_key.json \
		--build-arg PRIV_VALIDATOR_STATE=priv_validator_state.json \
		-t ghcr.io/product-science/tmkms-softsign-with-import:$(VERSION) \
		.

release: build-with-keygen

docker-push:
	@echo "pushing to GitHub Container Registry"
	@docker push ghcr.io/product-science/tmkms-softsign-with-keygen:$(VERSION)

	@if [ "$(SET_LATEST)" = "1" ]; then \
		@echo "Setting latest tag..."; \
		@docker tag ghcr.io/product-science/inferenced:$(VERSION) ghcr.io/product-science/inferenced:latest; \
		@echo "Pushing latest tag to GitHub Container Registry"; \
		@docker push ghcr.io/product-science/inferenced:latest; \
	fi
