# devshard module — see devshard/docs/testenv.md §8.8 (Phase 15 CI targets).
.PHONY: proto test integration-test stress-test fault-test
.PHONY: ci-unit ci-integration ci-scenarios ci-dep-check

proto:
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=devshard proto/devshard/v1/*.proto

test:
	go test ./... -v

integration-test:
	go test ./user/ -tags stress -run 'TestStress|TestFault' -v -timeout 30m -count=1

stress-test:
	go test ./user/ ./storage/ -tags stress -run TestStress -v -timeout 30m -count=1

fault-test:
	go test ./user/ -tags stress -run TestFault -v -timeout 10m -count=1

# ---- CI (testenv.md §8.8) ----------------------------------------

ci-dep-check: ## §8.4 rules 1–6. ≤ ~30s.
	@bash ./scripts/ci-dep-check.sh

ci-unit: ## §8.1 unit tests; full module, race detector. ≤ ~2–3m on CI.
	go test -count=1 -race -timeout=3m ./...

# §8.2: I1 + §8.7 in citest; I2–I10 remain future (see testenv.md §8.2).
# Uses -parallel 1 so host-port compose tests do not overlap.
ci-integration: ci-compose-smoke

ci-compose-smoke:
	go test -count=1 -parallel=1 -timeout=15m -tags=testenvci ./testenv/citest/...

ci-scenarios: ## §8.3 config sanity + room for cPoC cases (testenv/scenarios). ≤ ~15m budget when expanded.
	go test -count=1 -timeout=15m ./testenv/scenarios/...
