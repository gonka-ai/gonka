# Linting

`golangci-lint` runs over every Go module in the repository from a single config at the repository root, `.golangci.yml`. The ruleset started life in `devshard/cmd/gateway` and was lifted to the root unchanged apart from what had to become module-agnostic.

## How the config is found

`golangci-lint` walks up from the directory it runs in and takes the first config it meets, so `golangci-lint run ./...` inside any module lands on the root file. Nothing needs to be passed on the command line.

`inference-chain/.golangci.dont-panic.yml` is the one exception. It is the narrow `forbidigo` guard that forbids `panic` and `Must*` in consensus code, and because of the name it is never picked up implicitly — `.github/workflows/dont_panic.yml` passes it with `--config`. The module itself lints against the shared rules like everything else.

## Targets

| Target | Scope |
| --- | --- |
| `make lint` | `LINT_MODULES` — the modules that must stay green. Fails the build. |
| `make lint-fix` | The same modules, applying every fix the linters and formatters can apply. |
| `make lint-all` | Every module, never fails: the backlog behind `LINT_MODULES`. |

`LINT_MODULES` starts at `devshard` and widens one module at a time, as each one's backlog is cleared. Any target takes an override: `make lint LINT_MODULES=common`.

## Formatting

Three formatters run in a fixed order and each owns one job:

- `goimports` drops imports a fix orphaned and adds the ones it needs. `make lint-fix` calls `golangci-lint fmt` after the fix pass to run it, because its own findings are excluded from `run` and a fix pass therefore does not apply it.
- `gci` owns the grouping: standard library, third party, then the module being linted (`localmodule`, resolved from its own `go.mod`).
- `gofumpt` formats everything else. Its findings are excluded from `run`, because it reads a dotless first path segment (`common/...`) as standard library and would fight `gci` over it; `gci` runs last and settles the order either way.

## Known exceptions

`modernize`'s `fmtappendf` check is disabled. It reads the first argument of a `fmt.Sprint` call as a string constant and panics on `[]byte(fmt.Sprint(intConst))`, which `devshard/storage` uses for its advisory-lock namespace. Upstream disabled the same modernizer by default (golang/go#77581).
