# Upgrade Proposal: v0.2.13-devshard-v3

This proposal covers the devshard v3 release.

This is a devshard-only upgrade. It operates independently of full chain software upgrades. Once approved, v3 runs in parallel with the existing devshard runtimes.

The v3 runtime prepares brokers to keep serving inference during the v0.2.14 chain upgrade without depending on the deprecated classic API path. It also improves RAM utilization, fixes gateway runtime behavior, and enables safe switching between SQLite and Postgres storage.

## Upgrade Plan

The devshard runtime is upgraded through an on-chain params proposal, not a full chain software upgrade.

The proposal registers a new entry in `DevshardEscrowParams.approved_versions`:

- `name`: `v3`
- `binary`: `URL`
- `sha256`: `SHA256`

The release publishes the `devshardd` binary as a Gonka release artifact. If the on-chain proposal is approved, `versiond` automatically downloads the binary, verifies the sha256 hash, and starts an additional `devshardd` process inside the existing `versiond` container.

The new process is served under the `/devshard/v3` prefix. Existing devshard traffic can continue using earlier runtime prefixes until brokers switch traffic to v3. No mainnet restart or manual host steps are expected during this type of upgrade.

## Testing

The devshard v3 runtime was deployed and verified first. During testing, `node` and `api` containers were stopped while devshard traffic was running; active requests were allowed to finish, and new requests were successfully created and executed through `/devshard/v3`.

## Changes

### devshard

- Serve inference during the PoC validation phase on validation-inference-capable nodes [#1348](https://github.com/gonka-ai/gonka/pull/1348) by @qdanik, @gmorgachev.
- Enable standalone `devshardd` to continue serving during DAPI outages using an ML-node cache [#1417](https://github.com/gonka-ai/gonka/pull/1417) by @akup.
- Add per-escrow SQLite/Postgres storage routing and allow storage backend transitions without stopping existing sessions [#1417](https://github.com/gonka-ai/gonka/pull/1417) by @akup.
- Snapshot `validation_rate` at escrow creation so all group members bind the same validation rate [#1417](https://github.com/gonka-ai/gonka/pull/1417) by @akup.
- Fix a production host leak where failed session resolution left validation workers alive [#1417](https://github.com/gonka-ai/gonka/pull/1417) by @gmorgachev.

### gateway

- Collect gateway v3 runtime fixes, including runtime version stamping, request parameter validation, SSE handling, drain barriers, and gateway v2 cleanup [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @gmorgachev, @qdanik, @libermans, @a-kuprin.
- Add gateway observability and Prometheus scrape configuration for runtime, session, and host diagnostics [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @gmorgachev.
- Bound gateway runtime-build fan-out to avoid LCD 429 crash loops [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @qdanik.
- Settle depleted escrows after in-flight requests drain and retire runtime escrows safely [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @qdanik.
- Add MiniMax-M2.7 route support, per-model dispatch, tool-message handling, and reasoning-split support [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @qdanik.
- Skip escrow rotation for models absent from the network and recover orphaned rotation escrows after DB persist failures [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @qdanik.
- Validate and clamp chat-completion parameters at the gateway, including `n`, before forwarding to vLLM [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @qdanik.
- Reassemble fragmented SSE events before race-writer content classification [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @qdanik.

## Contributors (sorted alphabetically)

- @a-kuprin
- @akup
- @d-bogdan-engenious (testing)
- @gmorgachev
- @libermans
- @maria-mitina (testing)
- @qdanik
