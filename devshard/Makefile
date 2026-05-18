# devshard module — see devshard/docs/testenv.md §8.8 (Phase 15 CI targets).
.PHONY: proto test integration-test stress-test fault-test
.PHONY: ci-unit ci-integration ci-scenarios ci-container-e2e ci-dep-check
# ci-container-e2e runs all TestContainerE2E_HeightSync_* (phases A–C).

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

# §7.2: I1 + I2a + I2b + §7.7 in citest; I3–I10 remain future (see testenv.md §7.2).
# testenv/scripts/run-stack-citest.sh: gen config+compose, preflight, compose up,
# height-sync gate, then citest (compile+exec, TESTENV_REUSE_STACK=1).
ci-integration: ## Stack integration (same driver as make -C testenv citest-stack).
	bash ./testenv/scripts/run-stack-citest.sh

ci-scenarios: ## §8.3 config sanity + room for cPoC cases (testenv/scenarios). ≤ ~15m budget when expanded.
	go test -tags=dev -count=1 -timeout=15m ./testenv/scenarios/...

ci-container-e2e: ## Container height-sync E2E (compose + TestContainerE2E_HeightSync_*). Needs Docker.
	$(MAKE) -C testenv e2e
