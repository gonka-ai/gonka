BOOKWORM_HOST := root@192.168.111.25
BOOKWORM_PORT := 2022
BOOKWORM_DIR  := /tmp/gonka-quality-middleware
FORK_BRANCH   := feature/gip-semantic-cache-trust-layer
FORK_REMOTE   := git@github.com:Mayveskii/gonka.git

.PHONY: test push bookworm-test

## Run tests locally (requires go in PATH)
test:
	go test -v -count=1 ./...

## Push current branch to fork
push:
	cd ../.. && git push fork $(FORK_BRANCH)

## Pull latest from fork on bookworm and run tests
bookworm-test:
	ssh -p $(BOOKWORM_PORT) $(BOOKWORM_HOST) " \
		rm -rf $(BOOKWORM_DIR) && \
		git clone --depth=1 --branch=$(FORK_BRANCH) $(FORK_REMOTE) $(BOOKWORM_DIR) && \
		cd $(BOOKWORM_DIR)/examples/quality-middleware && \
		go test -v -count=1 ./... \
	"

## Commit, push to fork, then run tests on bookworm — one command
ci: push bookworm-test
