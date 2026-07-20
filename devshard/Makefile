.PHONY: proto test integration-test stress-test fault-test ci-testenv-unit ci-testenv-integration

proto:
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=devshard proto/devshard/v1/*.proto

test:
	go test ./... -v

# Phase 11 — testenv v2 unit smoke (no Docker). Run from repo root paths via sub-makes.
ci-testenv-unit:
	$(MAKE) -C ../common ci-testenv-runtimeconfig
	go test ./chainoracle/... -count=1
	go test ./runtimeparams/... -count=1
	go test ./runtimeconfig/... -count=1
	go test ./testenv/... -count=1
	$(MAKE) -C ../decentralized-api ci-testenv-nodemanager

# Phase 11 — Docker stack citest (dispatch / nightly). Requires Docker + linux devshardd.
ci-testenv-integration:
	$(MAKE) -C testenv build-devshardd
	$(MAKE) -C testenv citest-stack
	$(MAKE) -C testenv citest-grpc-transport
	$(MAKE) -C testenv citest-adversarial

integration-test:
	go test ./user/ -tags stress -run 'TestStress|TestFault' -v -timeout 30m -count=1

stress-test:
	go test ./user/ ./storage/ -tags stress -run TestStress -v -timeout 30m -count=1

fault-test:
	go test ./user/ -tags stress -run TestFault -v -timeout 10m -count=1
