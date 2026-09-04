# Linting

`golangci-lint` runs over every Go module in the repository from a single config at the repository root, `.golangci.yml`. The ruleset started life in `devshard/cmd/gateway` and was lifted to the root, then widened after a security review.

## How the config is found

`golangci-lint` walks up from the directory it runs in and takes the first config it meets, so `golangci-lint run ./...` inside any module lands on the root file. Nothing needs to be passed on the command line.

Two configs are deliberately named so that nothing picks them up implicitly, and both are passed with `--config`:

- `inference-chain/.golangci.dont-panic.yml` — the `forbidigo` guard that forbids `panic` and `Must*` in consensus code, run by `.github/workflows/dont_panic.yml`.
- `.golangci.deprecated.yml` — every use of a deprecated API, reported and never enforced. See below.

## Targets

| Target | Scope |
| --- | --- |
| `make lint` | `LINT_MODULES` — the modules that must stay green. Fails the build. |
| `make lint-fix` | The same modules, applying every fix the linters and formatters can apply. |
| `make lint-deprecated` | Deprecated APIs across `LINT_MODULES`. Prints, never fails. |
| `make lint-all` | Every module, including the ones not yet clean: the backlog behind `LINT_MODULES`. |

`LINT_MODULES` widens one module at a time, as each one's backlog is cleared. It currently holds `devshard`, `edge-api`, `proxy-ssl`, `versioned` and both `test-net-cloud` drivers; `common`, `decentralized-api` and `inference-chain` are still behind it, with roughly 400, 1300 and 1700 findings respectively — about half of them auto-fixable. Any target takes an override: `make lint LINT_MODULES=common`.

## Deprecated APIs

Deprecations sit outside the gate, in `.golangci.deprecated.yml`, at severity `warning`. A pinned dependency that deprecates something cannot turn the build red on its own, and the uses stay visible in one list instead of being buried under `//nolint` directives at each site. Today that list holds RIPEMD-160 (fixed by the Cosmos address derivation), `TimeslotAllocation` (read for wire compatibility, exactly as its deprecation note describes), and three testcontainers APIs whose replacements do not exist in the version we pin.

## Formatting

Three formatters run in a fixed order and each owns one job:

- `goimports` drops imports a fix orphaned and adds the ones it needs. `make lint-fix` calls `golangci-lint fmt` after the fix pass to run it, because its own findings are excluded from `run` and a fix pass therefore does not apply it.
- `gci` owns the grouping: standard library, third party, then the module being linted (`localmodule`, resolved from its own `go.mod`).
- `gofumpt` formats everything else. Its findings are excluded from `run`, because it reads a dotless first path segment (`common/...`) as standard library and would fight `gci` over it; `gci` runs last and settles the order either way.

## What the ruleset does and does not enforce

The set is chosen by yield, not by reputation. Rules that fired only to be suppressed were dropped: `bodyclose` cannot follow a test helper that closes the body, `sqlclosecheck` wants `defer` where SQLite has to close before the next statement on the same connection, `nilerr` misreads the "verdict in a bool, error in the response envelope" contract, and `usetesting` would move the stand's work directory out from under the compose file. `godot` and `prealloc` went the same way: neither caught a bug, and both cost readability.

Security rules are scoped to what the running service exposes. `gosec` and `noctx` are excluded from tests and from the stand, which open files and shell out by construction, and where a hanging request fails a test rather than holding a socket. The `test-net-cloud` drivers lose only `noctx`: they are a harness too, but they carry `gosec` decisions worth keeping. Inside `gosec`, `G404` and `G115` stay off because deterministic paths need `math/rand` and cosmos `Int` conversions are the SDK's business; `G104` is off because `errcheck` owns unchecked errors and knows our exclusion list; `G706` is off because everything we log is our own state.

`errcheck` carries an exclusion list rather than `//nolint` at each site: cleanup calls whose error nothing can act on — `Rows`/`Stmt`/`DB` close, `Tx.Rollback` for both `database/sql` and pgx, `os.File.Close`, `io.Copy` — plus our own stores and runtimes, which are closed on the error path where the original error is the one worth returning and a close failure could only mask it. It is also off in `_test.go`.

`depguard` carries one rule: `signing/` and `keymaterial/` may not import `math/rand`. Everywhere else `math/rand` is deliberate — on-chain determinism requires it, and `crypto/rand` there would break consensus.

`gochecknoinits` is excluded under any `x/` directory: Cosmos modules register their codecs from `init()`, and that is the framework's shape rather than ours.

`exhaustive` runs with `default-signifies-exhaustive`. A switch with a `default` has an answer for a member nobody has added yet; one without silently does nothing, and that is the case worth failing on. The setting takes the rule from 39 findings to the 9 switches that had no default at all.

`noctx` is enforced on the network cases it is named for — HTTP calls and listeners. Its `database/sql` findings, 126 of them, are excluded by message: the storage layer has no context to thread yet, and giving it one is its own change rather than a lint fix.

`contextcheck` was tried and dropped. Of its 73 findings, the 22 it called "non-inherited" were all correct by design — a fresh `context.Background()` for shutdown and for rollback on the error path, where the parent is already cancelled and inheriting it would be the bug; `if ctx == nil` guards; a deliberately detached cleanup root whose comment says so; and handler code passing `r.Context()` through a helper that wraps `context.WithValue`, which the rule cannot see through. The remaining 47 want a context threaded through two to five call levels, and four of those chains end inside `ApplyDiff` — a deterministic state transition that must not carry a cancellation source. Threading contexts through the storage and gateway layers is worth doing; it is a change of its own, not something this rule can drive.

## Known exceptions

`modernize`'s `fmtappendf` check is disabled. It reads the first argument of a `fmt.Sprint` call as a string constant and panics on `[]byte(fmt.Sprint(intConst))`, which `devshard/storage` uses for its advisory-lock namespace. Upstream disabled the same modernizer by default (golang/go#77581).

Two `testifylint` checks are disabled, both because they trade an exact assertion for an approximate one. `encoded-compare` rewrites the SSE assembly tests, which are about how fragments concatenate — `JSONEq` would pass on a differently spaced assembly. `float-compare` rewrites gauge values and parameter bounds that are exact by construction, in code that has to stay deterministic.

`go-require` stays on, and it earned its place: 57 assertions were running `require` inside goroutines and HTTP handlers, where `FailNow` from a non-test goroutine is undefined behaviour. They are `assert` now.
