# Devshard E2E Tests

This package contains Docker-backed end-to-end tests for `devshard`. The tests
run real HTTP between `devshardctl` and `devshard-host` containers, use a local
mock chain, and include Postgres in the smoke environment.

The Go tests own the service topology through `testcontainers-go`. The
`docker-compose.e2e.yml` file only starts the Go test runner container.

## Topology

Each test starts an isolated Docker network with:

- `mock-chain`: testenv gRPC mock chain (`testenv/cmd/mockchain`) seeded from
  `e2e/mock-chain-config.yaml`; serves escrow/participant queries on `:9090` and
  devshardctl phase-gate stubs on `:9191` (`/v1/epochs/*`).
- `postgres`: smoke CI storage dependency.
- `devshard-host-0..2`: three participant hosts with stub inference.
- `devshardctl`: OpenAI-compatible HTTP entry point for the test client.

Some recovery tests also attach named Docker volumes to host containers to
verify SQLite-backed restart behavior.

## Running Locally

Build E2E images and run the full suite:

```sh
make -C devshard e2e
```

Run only the test suite using already-built images:

```sh
make -C devshard e2e-test
```

Run one focused test:

```sh
DEVSHARD_E2E_DEBUG=1 docker compose -f devshard/docker-compose.e2e.yml run --rm e2e-test \
  go test ./e2e -run TestE2E_NonStreamingHappyPath -count=1 -v
```

## Debug Output

Enable debug logs with:

```sh
DEVSHARD_E2E_DEBUG=1 make -C devshard e2e-test
```

Debug mode prints E2E request/response details and settlement contracts. The
testcontainers Docker lifecycle logger is muted so output focuses on test
behavior rather than container create/start/stop noise.

## Reports

The Compose runner uses `gotestsum` and writes a JUnit report to:

```text
devshard/build/test-results/e2e/junit.xml
```

CI uploads this report as `devshard-e2e-junit-report` and publishes it as the
`Devshard E2E Tests` check.

## Test Files

- `non_streaming_test.go`: non-streaming OpenAI-style completions, cache
  behavior, and settlement-contract validation.
- `non_streaming_corner_cases_test.go`: non-streaming error and degraded-host
  behavior.
- `streaming_test.go`: SSE streaming shape, cache-hit streaming shape, and
  streaming/non-streaming cache isolation.
- `recovery_test.go`: SQLite volume restart and persistence scenarios.
- `protocol_test.go`: signature quorum and signed-nonce gossip convergence.
- `timeout_test.go`: refused-executor timeout votes, timeout transaction
  application, recovery, and finalization.
- `runtime_config_test.go`: chain-backed runtime configuration wiring.
- `containers_test.go`: Docker network/container setup and restart helpers.
- `testutil/`: HTTP clients, request helpers, assertions, and env defaults.

## Notes

E2E tests require `DEVSHARD_E2E=1`. The Compose runner sets this automatically.

Each test uses a unique Docker network name so stale networks from interrupted
runs do not break the next run. If Docker state still gets messy, prune stopped
containers/networks with Docker's standard cleanup tools before rerunning.
