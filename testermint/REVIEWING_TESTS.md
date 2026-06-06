# Reviewing Testermint Tests

This guide is for reviewing new or changed Testermint tests in pull requests.

The goal is to catch tests that are:

- semantically weak
- too timing-sensitive
- hard to debug when they fail
- coupled to incidental implementation details instead of product behavior

## Review goals

A good Testermint test should be:

- reliable: it should not fail just because routing, reconciliation, or block production was briefly slow
- discoverable: when it fails, the next person should be able to tell what happened and why
- behavior-focused: it should assert the product contract, not a transient intermediate state unless that state is itself the contract
- isolated: it should not silently depend on leftover cluster state, previous tests, or unrelated services

## Reliability checklist

### 1. Check that the test asserts the real invariant

Ask:

- What business or protocol behavior is this test actually meant to prove?
- Do the assertions match that behavior directly?
- Is the test asserting a proxy that can drift while the real behavior is still correct?

Common smell:

- asserting a node or participant reaches one exact status string when the real contract is slashing, settlement, reward distribution, or exclusion

Better:

- assert the economic or protocol outcome directly
- examples:
  - collateral was slashed
  - exemption usage incremented
  - total `coinsOwed` delta matches `actualCost`
  - all nodes eventually converge to the same DKG output

### 2. Watch for timing-hostile reads

Most flaky Testermint failures come from reading state too early.

Look for assertions immediately after:

- `disableNode(...)`
- `enableNode(...)`
- `initCluster(...)`
- epoch transitions
- upgrade boundaries
- post-reboot startup
- inference submission or claim submission

If the test reads immediately after one of those transitions, it often needs:

- `waitForStage(...)`
- `waitForNextInferenceWindow(...)`
- `waitForBlock(...)`
- or a bounded retry around a known transient response

Good pattern:

```kotlin
genesis.waitForBlock(10) { pair ->
    pair.api.getNodes()
        .first { it.node.id == nodeId }
        .state.currentStatus == "INFERENCE"
}
```

Bad pattern:

```kotlin
val node = genesis.api.getNodes().first { it.node.id == nodeId }
assertThat(node.state.currentStatus).isEqualTo("INFERENCE")
```

### 3. Prefer bounded waits over blind sleeps

`Thread.sleep(...)` is usually the wrong primitive in Testermint tests.

Prefer:

- `waitForBlock(...)`
- `waitForStage(...)`
- `waitForNextInferenceWindow(...)`

Why:

- they move with actual chain progress
- they fail with more context
- they are less sensitive to runner speed differences

Exception:

- a short sleep inside a narrowly scoped retry helper can be acceptable for bootstrap retries, especially around `initCluster(...)` on upgrade-heavy tests

### 4. Be careful with exact counts under concurrent load

Tests that submit parallel requests often become flaky when they require one exact rejection or success count.

Common examples:

- bandwidth limiter
- subnet parallelism
- multi-node inference routing

Prefer:

- lower bounds on meaningful behavior
- ratio or existence checks
- asserting that overload produced rejections, not that it produced one exact historical number

Good:

- "should reject several requests"
- "should reject at least one request"
- "should preserve isolated settlement across multiple sessions"

Risky:

- "must reject exactly 9"
- "the second node must own slot index 1"

### 5. Avoid coupling to incidental payload shape

Some test failures come from asserting the exact shape of a nested API payload when the test only needs a higher-level fact.

Common smell:

- reading `getActiveParticipants()` and asserting nested allocation details there because it happened to be convenient

Prefer:

- `getNodes()` for node state
- chain queries for chain state
- direct participant queries for participant balances/status

Rule of thumb:

- if the test is about node state, read node state
- if the test is about balances or collateral, read balances or collateral
- if the test is about inference outputs, read inference outputs

### 6. Treat known transient errors as retryable only when they are truly transient

Some failures are normal short-lived windows, especially:

- temporary `500 Internal Server Error`
- `After filtering participants the length is 0`
- `Inference never logged in chain`
- socket/container startup errors during reboot-heavy bootstrap

A retry is appropriate only if:

- the underlying action is expected to succeed once routing or startup catches up
- the retry is bounded
- unexpected exceptions still fail fast

Good retry shape:

```kotlin
runCatching { getInferenceResult(genesis) }
    .onFailure { error ->
        val retryable = error is FuelError && error.message.orEmpty().contains("500")
        if (!retryable) throw error
        genesis.waitForBlock(1) { true }
    }
```

Bad retry shape:

- catch-all retries that swallow unrelated product regressions

### 7. Keep retries local, not global

If one test or one category of tests needs startup retries, prefer a local helper in that test file.

Do not rush to add shared bootstrap retries in `DockerGroup` or common harness code unless:

- the failure is clearly systemic
- it reproduces across unrelated suites
- and it does not make other suites worse

This matters because broad bootstrap workarounds can hide real problems or destabilize previously healthy shards.

### 8. Use test-specific config surgically

Adjusting epochs, expiration windows, or session count is sometimes the right fix, but treat it as a tradeoff.

Safe use:

- extend epochs so a real state transition becomes observable
- reduce overload slightly so the test validates semantics instead of saturation behavior

Review questions:

- Does the change preserve the actual scenario?
- Did we just make the test slower, or did we make it more faithful to the intended behavior?
- Are we silently turning a stress test into a smoke test?

If a test used to be both:

- a semantic correctness test
- and a heavy stress test

then it may be better for CI gating to keep the semantic part and move the stress envelope elsewhere.

## Discoverability checklist

### 1. The test name should say what contract it covers

A reviewer should be able to answer this from the test name alone:

- what behavior is being validated?
- what kind of regression would break it?

Good:

- `a participant is slashed for downtime with unbonding slashed`
- `parallel subnet sessions with isolated settlement`

Weaker:

- `test edge case 2`
- `test after update`

### 2. Important transitions should be logged with `logSection(...)`

When a test crosses a major boundary, it should leave breadcrumbs.

Good section points:

- cluster bootstrap
- waiting for inference window
- disabling or enabling a node
- submitting an upgrade
- entering PoC
- starting a bad inference batch
- verifying final balances or slashing

That makes the per-test log readable without re-deriving the flow from source.

### 3. Failures should be diagnosable from the artifact log

A reviewer should ask:

- if this fails in CI, will the failure line tell us enough?
- do we log the relevant counts, balances, or status transitions before asserting?

Good:

- log successes/rejections/other errors in a limiter test
- log balance before and after claim
- log status and timeout count after each batch

Weak:

- one final assertion with no intermediate context

### 4. Prefer per-test helpers when they clarify intent

If a test needs special retry or readiness logic, a small local helper can improve discoverability.

Examples:

- `initUpgradeCluster(...)`
- `initVestingCluster(...)`
- `waitForInferenceRouting(...)`

That is often easier to review than repeating low-level retry code inline.

## Questions to ask in review

Use these when reviewing a changed Testermint test:

1. What exact product behavior does this test claim to validate?
2. Are we asserting that behavior directly, or using a brittle proxy?
3. Could this fail just because the chain, API, or routing layer is a block behind?
4. Does the test rely on one exact count or slot placement that is not actually contractual?
5. If the test adds retries, are they bounded and limited to known transient failures?
6. If the test changes epochs or concurrency, does it still cover the intended scenario?
7. If this fails in CI, will the test log be self-explanatory?

## Common acceptable fixes

These are usually good directions when stabilizing a flaky Testermint test:

- replace immediate assertions with bounded `waitForBlock(...)` checks
- assert final economic/protocol outcomes instead of incidental intermediate status
- tolerate documented transient startup or routing errors with narrow retries
- move retries into the specific flaky test file rather than shared bootstrap code
- relax an overfit numeric threshold while preserving the same invariant
- add targeted logging before important assertions

## Common risky fixes

These should trigger extra scrutiny:

- broad shared bootstrap retries in common harness code
- replacing a strong assertion with a vague existence check
- removing assertions without replacing them with a better invariant
- large tolerance increases with no observed evidence
- lowering concurrency so far that the original scenario is no longer being tested
- adding sleeps where a block- or stage-based wait would work

## Practical review workflow

When reviewing a new or changed Testermint test:

1. Read the test name and first comment block.
2. Identify the core invariant.
3. Scan for timing boundaries: bootstrap, reboot, epoch shifts, enable/disable, upgrade, inference claim.
4. Check whether waits are tied to chain progress rather than wall-clock sleep.
5. Check whether assertions match the true business outcome.
6. Check whether logs would make a CI failure understandable.
7. If the test uses thresholds or counts, ask whether they are semantic or just historical.

## When to escalate

Ask for a second look if:

- a test fix changes shared harness behavior
- a change removes stress or concurrency coverage substantially
- the new assertions are clearly weaker but not clearly more correct
- the test is now relying on retries for behavior that may actually be broken

In those cases, it may be worth comparing:

- a passing artifact vs a failing artifact
- local vs CI behavior
- or using the log examiner for larger log sets

