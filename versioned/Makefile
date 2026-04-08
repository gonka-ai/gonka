.PHONY: build test e2e testapp

build:
	mkdir -p build
	go build -o build/versiond ./cmd/versiond

test:
	go test ./... -v

testapp:
	mkdir -p build
	go build -o build/testapp ./e2e/testapp

e2e: build testapp
	docker compose -f e2e/docker-compose.yml up --build --abort-on-container-exit --exit-code-from tests
