.PHONY: all install build build-docker init clean-state mock-expected-keepers docker-push

BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git log -1 --format='%H')

# don't override user values
ifeq (,$(VERSION))
  VERSION := $(shell git describe --exact-match 2>/dev/null)
  # if VERSION is empty, then populate it with branch's name and raw commit hash
  ifeq (,$(VERSION))
    VERSION := $(BRANCH)-$(COMMIT)
  endif
endif

ldflags = \
  -X github.com/cosmos/cosmos-sdk/version.Name=inference-chain \
  -X github.com/cosmos/cosmos-sdk/version.AppName=inference-chaind \
  -X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
  -X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

install:
	@echo "Installing locally with ldflags..."
	@go install -ldflags "$(ldflags)" -mod=readonly ./cmd/inferenced/main.go

build:
	@echo "Building locally with ldflags..."
	@CGO_ENABLED=1 CC=gcc \
		go build -ldflags "$(ldflags)" -o ./build/inferenced ./cmd/inferenced/main.go

define DOCKER_BUILD
	@echo "--> building inferenced docker image"
	@echo "platform:  $(PLATFORM)"
	@echo "GOARCH:    $(GOARCH)"
	@echo "LDFLAGS:   $(ldflags)"
	@echo "GOOS:      $(GOOS)"

	@DOCKER_BUILDKIT=1 \
	docker build \
		--platform $(PLATFORM) \
		--build-arg LDFLAGS='$(ldflags)' \
		--build-arg GOOS=$(GOOS) \
		--build-arg GOARCH=$(GOARCH) \
		-f $(DOCKER_FILE) \
		.. \
		-t $(DOCKER_TAG)
endef

build-docker:
	rm -rf build/
	$(eval PLATFORM=linux/amd64)
	$(eval GOOS=linux)
	$(eval GOARCH=amd64)
	$(eval DOCKER_FILE=Dockerfile)
	$(eval DOCKER_TAG=gcr.io/decentralized-ai/inferenced)
	$(DOCKER_BUILD)

clean-state:
	rm -rf "$$HOME/.inference"

docker-push:
	docker push gcr.io/decentralized-ai/inferenced

build-v2-test:
	$(eval PLATFORM=linux/amd64)
	$(DOCKER_BUILD)
	@echo "--> clearing out ../public-html/v2/inferenced"
	@rm -rf ../public-html/v2/inferenced/*
	@mkdir -p ../public-html/v2/inferenced
	@echo "--> copying built inferenced binary to ../public-html/v2/inferenced/inferenced"
	@cp ./build/inferenced ../public-html/v2/inferenced/inferenced
	@echo "--> zipping inferenced binary"
	@zip -j ../public-html/v2/inferenced/inferenced.zip ../public-html/v2/inferenced/inferenced
	@echo "--> generating shasum for inferenced.zip"
	@shasum -a 256 ../public-html/v2/inferenced/inferenced.zip
