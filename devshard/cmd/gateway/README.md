# devshard gateway

The devshard gateway is an OpenAI-compatible chat-completions endpoint in front of the decentralised inference network. For each request it commits a nonce on an escrow, races the request across several hosts, streams the first real answer to the client and settles every nonce it committed, including the ones that served nobody.

A nonce costs the escrow money from the moment it is committed, whether or not a host answers. Most of the code exists to keep that obligation: a failed request, a lost race and a shutdown all still owe the chain a vote or a settlement.

## Build, test and run

All commands run from `devshard/cmd/gateway`. You need Go 1.25.9 or later; linting needs golangci-lint v2.15 or later; images need Docker.

Build the binary into `devshard/bin/gateway`. `GATEWAY_VERSION` is required because it is hashed into every settlement the gateway signs ([`CONTRIBUTING.md`](./CONTRIBUTING.md), "Before you push"):

```bash
make build GATEWAY_VERSION=<version>
```

Test:

```bash
make test       # every gateway package, no race detector
make test-race  # the same under -race
make verify     # test-race and golangci-lint: the gate before a push
go test ./registry -run '^TestADrainedEscrow' -race -count=1 -v  # one test
```

Run the compose stack, which joins the node's `join_default` network and answers on `127.0.0.1:18080`:

```bash
make image GATEWAY_VERSION=<version>   # builds ghcr.io/gonka-ai/devshard-gateway-v4:stage locally
cd deploy
cp config.devshard-gateway.env.template config.devshard-gateway.env
$EDITOR config.devshard-gateway.env    # chain endpoints, API keys, admin key
export GATEWAY_IMAGE=ghcr.io/gonka-ai/devshard-gateway-v4:stage
docker compose -f docker-compose.devshard-gateway.yml up -d
curl -s localhost:18080/v1/status
```

The binary takes no flags and reads its whole configuration from the environment. [`deploy/README.md`](./deploy/README.md) lists the variables that have no working default and explains the stack.

## Documentation map

| If you want to | Read |
| --- | --- |
| run the compose stack | [`deploy/README.md`](./deploy/README.md) |
| look up a route, an auth tier, the boot or the shutdown order | [`docs/operations.md`](./docs/operations.md) |
| look up an environment variable, an admin override or a model filter setting | [`docs/configuration.md`](./docs/configuration.md) |
| look up a log line, a metric or a state route | [`docs/observability.md`](./docs/observability.md) |
| release, roll back, back up, settle or troubleshoot | [`docs/runbook.md`](./docs/runbook.md) |
| see how the packages fit together | [`docs/architecture.md`](./docs/architecture.md) |
| follow one request from socket to settled nonce | [`docs/request.md`](./docs/request.md) |
| understand the race, routing, escrows, capacity or the nonce ledger | [`docs/README.md`](./docs/README.md) lists each design document |
| know the invariants and the non-goals | [`docs/rules.md`](./docs/rules.md) |
| change code | [`CONTRIBUTING.md`](./CONTRIBUTING.md), then the `README.md` of the package you touch |
| find the protocol rules the gateway takes from `gatewayprotocol` | [`../../gatewayprotocol/README.md`](../../gatewayprotocol/README.md), and [`../../docs/gateway-protocol-library.md`](../../docs/gateway-protocol-library.md) for what belongs there |

## Packages

Each package has a `README.md` that states what the package owns, its contract with the rest of the gateway and the rules a contributor must keep. The import graph is in [`docs/architecture.md`](./docs/architecture.md).

| Package | What it owns |
| --- | --- |
| [`filters/`](./filters/README.md) | the request and response boundary: parameter rules, model profiles, response folding and stripping |
| [`api/`](./api/README.md) | HTTP: routes, auth, admission, cache, the client writer, error mapping, the admin surface |
| [`scheduler/`](./scheduler/README.md) | which escrow, which host, which nonce — and burning the nonces nobody can serve |
| [`engine/`](./engine/README.md) | the speculative race: attempts, escalation, crowning, streaming, and the votes the losers owe |
| [`registry/`](./registry/README.md) | the live escrow set and the sessions that dispatch on it |
| [`quorum/`](./quorum/README.md) | which timeout votes can pass: the arithmetic the vote queue and the sweep share |
| [`escrow/`](./escrow/README.md) | escrow creation, rotation, depletion, settlement, retirement, crash recovery |
| [`liquidity/`](./liquidity/README.md) | one escrow's money by class — free, returning, late, stuck — and whether it is full, starved or idle |
| [`funding/`](./funding/README.md) | one model's planned creates and retires from its money, demand, budget and rate |
| [`chain/`](./chain/README.md) | all blockchain input and output, and the epoch phase snapshot |
| [`tokenizer/`](./tokenizer/README.md) | a prompt's tokens with the model's pinned `tokenizer.json`, for the boundary's context check |
| [`limits/`](./limits/README.md) | admission, the per-host congestion windows, and the chain-weight capacity model |
| [`perf/`](./perf/README.md) | per-host history, outlier ejection, capability refusal counts |
| [`accounting/`](./accounting/README.md) | the per-nonce ledger and the findings derived from it |
| [`nonces/`](./nonces/README.md) | what feeds that ledger: live events, chain diffs, the sweep |
| [`journal/`](./journal/README.md) | one ordered path for race, request and ledger events to their log line or ledger fact |
| [`warmup/`](./warmup/README.md) | teaching a newly published escrow to its own group |
| [`shadow/`](./shadow/README.md) | probing the participants that serve no client |
| [`heights/`](./heights/README.md) | carrying mainnet height into the escrow's log: the courier, and the optional follower |
| [`hostping/`](./hostping/README.md) | pinging the live escrows' hosts for reachability and clock drift |
| [`hostrpc/`](./hostrpc/README.md) | polling the live escrows' hosts for the counters their rate limiters keep |
| [`store/`](./store/README.md) | control-plane state in SQLite |
| [`config/`](./config/README.md) | the immutable configuration snapshot and its atomic holder |
| [`env/`](./env/README.md) | every gateway environment variable; the runtime-params feed reads its own through `devshard/runtimeparams` |
| [`metrics/`](./metrics/README.md) | the Prometheus registry and every collector |
| [`app/`](./app/README.md) | the composition root: builds every layer into one process, boots and stops it |
| [`scenarios/`](./scenarios/README.md) | the scenario tests: the composed gateway against a fake chain and in-process hosts on fake time |

The protocol rules a host, a verifier or the chain would judge are not a package here: they live in [`devshard/gatewayprotocol`](../../gatewayprotocol/README.md).

The package root holds only `main.go`, which copies the stamped version to `app.Version` and calls `app.Main`.
