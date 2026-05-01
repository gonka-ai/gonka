# `devshard/testenv/spec`

Manual test specifications that complement the Go test suite. These
documents describe what to type on a laptop to verify behaviour that
`go test` can't reach — docker-compose orchestration, live-reload
inside containers, remote debugger wiring, and end-to-end HTTP flows.

Each spec is self-contained and can be run independently. They assume
the reader has followed the one-time setup in
[`../README.md`](../README.md) at least once.

## Command-based tests (what to run)

Step-by-step shell commands (compose, `make dev-up`, `curl` to the
operator proxy) are **not** in this README — they live entirely inside
each spec:

| Goal | Where |
|------|--------|
| Bring up stack, live-reload check, **inference send** | [`smoke-test.md`](smoke-test.md) — §0–2 setup, §3 live-reload, **§4 inference** |
| **≥20 inferences, concurrent** | Same file, **§4.2b** (after §4.1 brings up `devshardctl` on `:8081`) |

Minimum manual inference check: follow §4.1 then **§4.2b** so at least
20 chat-completion requests hit the proxy **in parallel** (`wait` at
the end). §4.2 remains the small sequential sanity loop; §4.3 still
applies to confirm fan-out in logs.

## Specs

| File                              | What it covers                                                         |
|-----------------------------------|------------------------------------------------------------------------|
| [`smoke-test.md`](smoke-test.md)  | Live-reload sanity + multi-inference fan-out across the 4 default hosts|

## When to add a new spec here

- The check requires a running container stack (docker-compose).
- The failure mode is ergonomic rather than functional (e.g. "port
  isn't reachable from my IDE", "air rebuild takes 10 minutes"). Those
  regressions are hard to catch in CI without a real Docker daemon.
- The happy path is short and deterministic enough to run by hand in
  under 5 minutes.

Behaviour that *can* be pinned by a Go test belongs there instead —
see `testenv/devoverlay_test.go`, `testenv/dockerfiles_test.go`, and
the per-package unit tests. The specs here are the fallback, not the
primary signal.
