.PHONY: generate generate-api generate-proto build test check-gen

generate: generate-api generate-proto

generate-api:
	go generate ./queryapi/...

generate-proto:
	go generate ./nodemanager/...

build:
	go build ./...

test:
	go test ./... -v

check-gen: generate
	git diff --exit-code queryapi/gen/ nodemanager/gen/
