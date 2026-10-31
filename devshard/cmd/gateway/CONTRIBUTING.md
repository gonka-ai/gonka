# Working on the gateway

How code in `devshard/cmd/gateway` is built, checked, laid out, named and tested. Every change is reviewed against these rules; what the gateway does and why lives in [`README.md`](./README.md) and [`docs/`](./docs/). Pull requests follow the repository's [`CONTRIBUTING.md`](../../../CONTRIBUTING.md), "Pull request lifecycle".

## Prerequisites

- Go 1.25.9 or later, the version `devshard/go.mod` names.
- golangci-lint v2.6 or later: [`.golangci.yml`](./.golangci.yml) is a version 2 config and enables `modernize`.
- Docker, only for `make image` and the e2e stand.

## Before you push

No CI job runs this gate, so running it is the author's job. Run from this directory; `golangci-lint run -v` prints the config file it used.

```
cd devshard/cmd/gateway
make verify                                        # go test -race, golangci-lint
golangci-lint run --new-from-merge-base=main ./... # only what the branch introduced
golangci-lint fmt ./...                            # gofumpt and gci, as the config sets them
```

One test, or the benchmarks of one package:

```
go test ./registry -run '^TestADrainedEscrow' -race -count=1 -v
go test ./journal -run '^$' -bench . -benchmem
```

`-count=1` makes every run execute: a cached `ok` says nothing about a race or ordering bug that shows only on some runs.

Build binaries with `make build GATEWAY_VERSION=…` or `make image GATEWAY_VERSION=…`, never a plain `go build -o`; both refuse to run without the version. `GATEWAY_VERSION` is stamped into `devshard/types.buildStateRootProtocolVersion`, which is hashed into every settlement, so an unstamped binary signs settlements the network computes differently. `make clean` removes the binary, `coverage.out` and `junit-report.xml`.

A `//nolint:<linter> // reason` is a last resort and carries its reason; `nolintlint` refuses one without.

## Load simulation

`app/loadsim_test.go` runs the gateway exactly as `run()` composes it — engine, scheduler, nonce ledger and its sweep, journal, metrics — against escrows served by real `host.Host` instances living in the same process, and drives streaming chat traffic at it while it profiles. Each escrow's session writes to its own SQLite store in a temporary directory, as a served session does, because the store's writes are part of what a nonce holds the session lock for; the run ends with one line per session step (`user.StepTimer`) saying how often it ran, its mean and its longest. It is behind the `loadsim` build tag, so `go test ./...` and `make verify` never run it: it is a measurement, and it loads the machine it runs on. The `LOADSIM_*` variables are read by the test alone; the gateway knows none of them. A long run is heavy: two escrows at thirty requests a second reach nine cores within eight minutes, because the cost of every diff grows with the escrows' live windows, so watch the machine rather than the clock.

```bash
LOADSIM_ESCROWS=8 LOADSIM_RPS=30 LOADSIM_DURATION=60s LOADSIM_PROFILE_DIR=/tmp/loadsim \
  go test -tags loadsim -run TestLoadSimulation -count=1 -timeout 10m -v ./app
go tool pprof -top -cum -nodecount=40 /tmp/loadsim/cpu.pprof
```

| Variable | Default | What it sets |
| --- | --- | --- |
| `LOADSIM_ESCROWS` | 4 | escrows published at boot, one in-process group each |
| `LOADSIM_GROUP_SIZE` | 4 | hosts per escrow |
| `LOADSIM_RPS` | 30 | requests per second, sent whatever the gateway answers (about 2.6 million a day) |
| `LOADSIM_WARMUP` | 5s | traffic before measuring, so the scheduler and the ledger reach steady state |
| `LOADSIM_DURATION` | 30s | the measured window |
| `LOADSIM_INFERENCE_LATENCY` | 2s | how long each host takes to answer, which is what fills the in-flight windows |
| `LOADSIM_STREAM_TOKENS` | 0 | when set, each host streams that many content chunks spread over `LOADSIM_INFERENCE_LATENCY`, then a usage chunk, instead of answering from `stub` in one piece |
| `LOADSIM_MAX_IN_FLIGHT` | 512 | requests open at once; an arrival past it is counted as shed, not sent |
| `LOADSIM_REPORT_EVERY` | 1m | how often the run logs the last interval's cost beside the escrows' mean live-inference count |
| `LOADSIM_PROFILE_DIR` | a temporary directory | where `cpu`, `mutex`, `block` and `heap` profiles land |

The run reports answered requests and their statuses, latency quantiles, process CPU and allocations per answered request, and the GC cycles of the window. Every `LOADSIM_REPORT_EVERY` it also logs the interval's own cost next to the mean number of live (unsealed) inferences per escrow, which is what shows a cost growing with the window: give fewer escrows the same rate and let it run, and the window reaches a production size sooner.

What it does not measure:

- **Hosts and gateway share one process.** Their CPU lands in one profile, told apart by package: `devshard/host`, and the host side of `devshard/state`, are the hosts; `cmd/gateway/...` and `devshard/user` are the gateway. The host-side transport is not there at all — `user.InProcessClient` calls the host directly, so HTTP to the hosts, TLS and signature transport cost nothing here.
- **No GPU.** The hosts answer from `stub` after a fixed delay.
- **No heartbeat.** A height-sync turn needs a height, and the only source is the anchors hosts return through the HTTP transport, which `InProcessClient` skips: started here, every turn is skipped with `cause=no_height`. The loop is therefore not started, and heartbeat cost has to be read off a stand whose hosts carry height sync.
- **The chain is a fake.** Epoch and participants come back empty, so weights, PoC phases, escrow creation and settlement transactions never run, and an escrow that depletes cannot be replaced. Each escrow is funded far beyond any run.
- **A few timeout votes come up short.** A handful of speculative nonces per run end `vote_weight_short`, and the races owing those votes outlive the ten-second shutdown grace, so the run usually ends with `serve()` reporting abandoned races. The test logs it rather than failing: it happens after the measured window.
- **Every reply logs a "not cached" warning.** `InProcessClient` streams without a `finish_reason`, so the reply cache refuses it as unfinished. Prompts are unique per request, so nothing would be served from the cache either way.

## Where code goes

- **One concern per package.** [`README.md`](./README.md), "The layers", says which package owns what; a package's own `README.md` says what it owns, what it does not, and its boundaries. A change that does not fit an owner's description is in the wrong package, or the description changes with it.
- **`app` is the composition root.** [`README.md`](./README.md), "The composition root", says what each of its files is for. Domain rules live in a layer package; the package root holds only the binary's `main.go`.
- **Changes stay inside `cmd/gateway`.** The devshard libraries (`devshard/user`, `devshard/state`, `devshard/transport`, `devshard/heightsync`) and the chain are owned upstream. When the gateway needs different behaviour from them, adapt on the gateway's side — a wrapper such as `registry.sessionHandle`, a consumer-side interface — and record a defect in the library in [`docs/findings.md`](./docs/findings.md).
- **Wire strings live in the package's `vocabulary.go`, type and all.** A metric label value, a log field value, a reason carried between packages: a named string type and its constants, passed as that type rather than `string`.
- **Interfaces are declared by their consumer**, as small as the consumer needs.
- **Dependencies.** `common`, `inference-chain` and the devshard libraries come through `replace` in `devshard/go.mod`. A new third-party module needs a reason in the pull request and a `go mod tidy` from `devshard/`.

## File layout

- Top to bottom: package doc, imports, constants, variables, types, constructors, methods, helpers.
- Imports in three groups — standard library, third-party including `common/...`, then `devshard/...` — which `gci` enforces.
- A Go source file other than a test past five hundred lines is split along the seam its declarations already have. A test file follows the file it tests. Markdown and other docs carry no line limit.

## Naming

- Real words, no abbreviations: `escrowID`, `participant`, `inFlight`, never `eid`, `p`, `n`. Single letters only for trivial indices, coordinates, method receivers (one or two letters, the same on every method of a type) and the idiomatic `w`/`r` of an HTTP handler.
- Functions are verbs, booleans read as predicates, collections are plural.
- A rename carries through the docs, the test names and the test failure messages in the same change.

## Comments and documentation

These rules bind new and changed code; an existing comment in a file you touch moves into the docs.

- **No comments in code.** The only exception is the doc comment on an exported identifier: one sentence, starting with the name, ending in a period (`godot` enforces the period) and, where there is more to say, a pointer — `// HoldSettlement: see README.md, "The published set and its readers".`
- No comments inside function or struct bodies and none on unexported identifiers. Every reason goes into the docs.
- A package's own behaviour goes in its `README.md`; behaviour that crosses packages goes in `docs/*.md`, indexed in [`docs/README.md`](./docs/README.md).
- Markdown: one line per paragraph. Cite code by file and function (`registry/views.go`, `Registry.HoldSettlement`), never by line number. Describe how the system works now — no dated notes, no "previously".
- A behaviour change updates the doc that describes it in the same change.

## Errors, logging, metrics, configuration

- **Errors.** Sentinels are `ErrXxx` and error types `XxxError` (`errname`), declared together in the package, as in `scheduler/errors.go`. Error strings are lowercase with no trailing punctuation; a wrap names the step (`dialing chain grpc %s: %w`); a sentinel with context comes first (`%w: escrow %s is parked for settlement`). Inspect with `errors.Is` / `errors.As`, never by string. On the money path an error is returned, not logged.
- **Logging.** The lifecycle packages — `engine`, `scheduler`, `registry`, `escrow`, `perf`, `limits`, `chain`, `warmup`, `api` and `app/observers.go` — never call `logging.*`; a line goes through the journal via the narrator interface the producer declares (`journal/guard_test.go` fails otherwise). Every key comes from `internal/logkey` (`journal/keys_test.go` fails on an undeclared key). [`journal/README.md`](./journal/README.md) names the files that may log directly.
- **Metrics.** Families are `devshard_gateway_<noun>_<unit>` — counters end in `_total`, durations in `_seconds` — declared only in `metrics/`. No nonce or request id as a label; escrow ids only where [`docs/rules.md`](./docs/rules.md), "11. Labels, ordering and determinism", allows. A new or renamed family updates [`docs/operations.md`](./docs/operations.md), "Metrics"; a removed one gets a row in "Metric changes".
- **A new knob.** Read the variable in `env/env.go` only, as a strict `GATEWAY_*`; default it in `config/defaults.go`, merge it in `config/build.go`, bound it in `config/validate.go`, add it to `config.Overrides` if an admin may change it at runtime, and document it in `config/README.md` and `docs/operations.md`. The runtime-params `DEVSHARD_*` knobs are the one exception ([`env/README.md`](./env/README.md)).

## Concurrency and context

- `context.Context` is the first parameter and is honoured. It is not stored in a struct, except a context held only as a cancellation signal (`engine/drain.go`, `drain`).
- Every goroutine has an owner that waits for it and an exit path; a component that starts goroutines exposes a way to stop and wait, and its package's tests run `leakcheck.VerifyTestMain`.
- Take a lock and `defer` its release on the next line, unless the lock must be dropped before a call out to a sink or a subscriber; then unlock explicitly, with no return between the two.
- Boot and shutdown order is a contract ([`docs/rules.md`](./docs/rules.md), "6. Boot and shutdown order is a contract").

## Tests

- **One behaviour per test, named as the sentence it proves:** `TestADrainedEscrowClosesOnlyAfterTheFinalizeRunningOnIt`. The `TestSubject_Behaviour` form is fine where a subject groups several tests. A test that pins a past bug says so in its name.
- **Every test carries a `Test flow` block and no other comment:**

  ```go
  // Test flow:
  //  1. Publish an escrow and take one request hold on it.
  //  2. Retire it, then release the request while a finalize runs.
  //  3. Assert the session is not closed until the finalize returns.
  func TestADrainedEscrowClosesOnlyAfterTheFinalizeRunningOnIt(t *testing.T) {
  ```

  Numbered, imperative, one line per step, saying what the test does. Nothing inside a test body; helpers and fakes get at most one short doc line.
- **Failure messages** read `Func(args) = got, want expected`: `t.Fatalf("Finalize(1) = %v, want nil", err)`. `testify/require` is used where it reads better; do not mix both styles in one test.
- **Deterministic.** Inject the clock and never `time.Sleep` to wait for work: wait on the component's own signal — a `WaitGroup`, a channel, a hook in a fake — or use `testing/synctest`. Use `t.Context()`, `t.Setenv` and `t.TempDir` (`usetesting` enforces them). Call `t.Parallel()` where the test shares nothing.
- **Table-driven tests** name their subtests; the loop variable is `testCase`.
- **Fakes** implement the consumer-side interface; a real in-process session is preferred where the behaviour under test lives in the session.
- **A bug fix starts with a failing test**, shown failing for the reason the bug gives. An assertion that passed before the fix is checked by mutation: break the fix, watch the test fail, restore it.
- **End to end.** Tests that need the running stand — mock chain, real hosts, containers — live in [`devshard/e2e`](../../e2e/), named `TestE2E_Gateway…`, in the same `Test flow` format. `make -C devshard e2e` builds the images and runs them; [`devshard/e2e/README.md`](../../e2e/README.md) shows how to run one.
- **End-to-end gates.** Every e2e test skips unless `DEVSHARD_E2E=1`, which the compose runner sets. Two more gates keep the long scenarios out of that default run, and the runner passes neither through, so set them on the run itself. `DEVSHARD_E2E_SLOW=1` adds the gateway scenarios that wait out a protocol deadline (`TestE2E_GatewayAccountsATimeoutItsVerifiersCouldNotDecide`, `TestE2E_GatewayAppliesTheTimeoutOfAHostThatReceiptsAndStalls`, `TestE2E_GatewayAccountsAStalledNonceEvenWhenTheGroupCallsItARefusal`) and the slow scenarios of the parity test. `DEVSHARD_E2E_PARITY=1` runs `TestE2E_DriverParity`, which boots the stand twice per scenario to compare devshardctl and the gateway on the same traffic. From the repository root, after `make -C devshard e2e-images`:

  ```sh
  docker compose -f devshard/docker-compose.e2e.yml run --rm -e DEVSHARD_E2E_SLOW=1 e2e-test go test ./e2e -run TestE2E_Gateway -count=1 -v -timeout 30m
  docker compose -f devshard/docker-compose.e2e.yml run --rm -e DEVSHARD_E2E_PARITY=1 -e DEVSHARD_E2E_SLOW=1 e2e-test go test ./e2e -run TestE2E_DriverParity -count=1 -v -timeout 60m
  ```

  Drop `-e DEVSHARD_E2E_SLOW=1` from the second command to skip the parity scenarios that wait out a protocol deadline.

## Scenario harness

[`scenarios/`](./scenarios/) runs the composed gateway inside a `testing/synctest` bubble against a fake chain and real in-process hosts, so an escrow's whole life runs on fake time in seconds of wall time. Add a test there for every lifecycle change, named as the sentence it proves; give it a row in [`scenarios/README.md`](./scenarios/README.md), which also says how to run them and how a known bug is pinned.

## Commits

Conventional commits scoped to the gateway, one topic each: `fix(gateway): keep a draining escrow's session open under a settlement still running on it`. The subject names the behaviour, not the edit; detail goes in the body.
