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

# Update the ldflags with the app, client & server names
ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=inference-chain \
	-X github.com/cosmos/cosmos-sdk/version.AppName=inference-chaind \
	-X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
	-X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

BUILD_FLAGS := -ldflags '$(ldflags)'

define DOCKER_BUILD
    @echo "--> deleting old ./build/minid"
	@rm ./build/minid
	@echo "--> building minid binary locally, outside of docker"
	@env GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o ./build/minid ./cmd/minid/main.go
	@echo "--> building minid docker image. platform: $(PLATFORM)"
	@docker build --platform $(PLATFORM) . -t minid
endef

install:
	@echo "--> ensure dependencies have not been modified"
	@go mod verify
	@echo "--> installing inferenced"
	@echo "BUILD_FLAGS = $(BUILD_FLAGS)"
	@go install $(BUILD_FLAGS) -mod=readonly ./cmd/inferenced/main.go

build:
	@echo "--> ensure dependencies have not been modified"
	@go mod verify
	@echo "--> building inferenced"
	@echo "BUILD_FLAGS = $(BUILD_FLAGS)"
	@go build $(BUILD_FLAGS) -o ./build/inferenced ./cmd/inferenced/main.go

init:
	./scripts/init.sh

clean-state:
	rm -rf $HOME/.inference
