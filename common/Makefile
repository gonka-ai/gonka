.PHONY: generate generate-proto build test check-gen ci-testenv-runtimeconfig

generate: generate-proto

generate-proto:
	go generate ./nodemanager/...

build:
	go build ./...

test:
	go test ./... -v

# Phase 11 — runtimeconfig server + client unit tests (testenv CI).
ci-testenv-runtimeconfig:
	go test ./runtimeconfig/... ./runtimeconfig/client/... -count=1
	go test ./chain/... -count=1

check-gen: generate
	git diff --exit-code nodemanager/gen/
