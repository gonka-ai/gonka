VERSION ?= $(shell git describe --always)
SET_LATEST ?= 0
SET_LATEST := $(shell if [ "$(SET_LATEST)" = "1" ]; then echo 1; else echo 0; fi)
DOCKER_TAG := $(if $(findstring 1,$(SET_LATEST)),latest,$(VERSION))

build-with-keygen:
	docker build \
		-f ./docker/Dockerfile_keygen \
		--build-arg TMKMS_TOML=./template/tmkms.toml \
		-t gcr.io/decentralized-ai/tmkms-softsign-with-keygen:$(DOCKER_TAG) \
		.

build-with-import:
	docker build \
		-f ./docker/Dockerfile_import_keys \
		--build-arg TMKMS_TOML=./template/tmkms.toml \
		--build-arg PRIV_VALIDATOR_KEY=priv_validator_key.json \
		--build-arg PRIV_VALIDATOR_STATE=priv_validator_state.json \
		-t tmkms-softsign-with-import:$(DOCKER_TAG) \
		.

push-keygen-image: build-with-keygen
	@docker push gcr.io/decentralized-ai/tmkms-softsign-with-keygen:$(DOCKER_TAG)

