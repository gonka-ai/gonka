# Upgrade Proposal: v0.2.14

This PR prepares the v0.2.14 release.

The mainnet chain/API work focuses on PoC duplicate-artifact protection, early share detection, classic inference API deprecation (disabling `/v1/chat/completions` billing on mainnet and removing embedded `/v1/devshard` from the API binary), reward recipient routing, and upgrade-time safety fixes.

The devshard part prepares the v3 runtime so brokers can serve inference during the chain upgrade without depending on the deprecated classic API path, improving RAM utilization and enabling safe switching between SQLite and Postgres storage.

## Upgrade Plan

The node binary is upgraded through an on-chain software upgrade proposal. Existing hosts are not required to manually update their `api` or `node` containers as part of the upgrade.

A separate devshard v3 release from this branch will be proposed and rolled out before the mainnet chain upgrade. Brokers who switch inference traffic to `/devshard/v3` ahead of time can keep serving inference while the chain upgrade runs.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. The devshard v3 release is proposed and rolled out before the mainnet chain upgrade.
3. Brokers switch inference traffic to `/devshard/v3`.
4. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

## Changes

### inference-chain / decentralized-api

- Replace the PoC artifact store with SMST-based nonce indexing to prevent duplicate artifacts, with benchmarks, params, and snapshot rebuild fixes [`9a63468`](https://github.com/gonka-ai/gonka/commit/9a6346862e9b365db7994cfa2f07aa81a2a5f958), [`22a21b0`](https://github.com/gonka-ai/gonka/commit/22a21b0b6d893200af0a88f5bb551d238c64f556) by @gmorgachev, @DimaOrekhovPS, @0xMayoor.
- Add DAPI-side early-share detection as an additional security layer, later reworked to check inclusion by nonce instead of dense index so it stays correct against the SMST store [#1382](https://github.com/gonka-ai/gonka/pull/1382), [#1416](https://github.com/gonka-ai/gonka/pull/1416) by @libermans, @DimaOrekhovPS, @gmorgachev.
- Remove deprecated classic decentralized-api inference paths and bind devshard routes to runtime versions [#1386](https://github.com/gonka-ai/gonka/pull/1386) by @gmorgachev.
- Track the last software upgrade height and fix disabling cPoC in the window around chain upgrades [#1268](https://github.com/gonka-ai/gonka/pull/1268) by @patimen.
- Seed maintenance-window params in the v0.2.14 handler with maintenance disabled by default, so governance can enable it later [#998](https://github.com/gonka-ai/gonka/pull/998), [#1428](https://github.com/gonka-ai/gonka/pull/1428) by @Ryanchen911, @DimaOrekhovPS.
- Zero mint inflation during upgrade, burn the fee collector base-denom balance, and add Dahl (@paranjko) to allowed devshard escrow creators [#1418](https://github.com/gonka-ai/gonka/pull/1418) by @gmorgachev.
- Add on-chain configurable reward claim recipients and apply them to stale devshard escrow payouts [#889](https://github.com/gonka-ai/gonka/pull/889), [#1377](https://github.com/gonka-ai/gonka/pull/1377) by @alancapex, @DimaOrekhovPS.
- Prevent reward payments for incoming delegations from excluded hosts [#1378](https://github.com/gonka-ai/gonka/pull/1378) by @gmorgachev.
- Add a total supply endpoint for integrations that need plain-text GONKA supply [#1346](https://github.com/gonka-ai/gonka/pull/1346) by @DimaOrekhovPS.
- Sync bridge block progress so Ethereum transactions are not lost if nodes restart during upgrade [#1376](https://github.com/gonka-ai/gonka/pull/1376) by @GLiberman.
- Smaller settlement, validation, fee, and setup-report fixes: [#1100](https://github.com/gonka-ai/gonka/pull/1100), [#1101](https://github.com/gonka-ai/gonka/pull/1101), [#1129](https://github.com/gonka-ai/gonka/pull/1129), [#1160](https://github.com/gonka-ai/gonka/pull/1160), [#1253](https://github.com/gonka-ai/gonka/pull/1253), [#1255](https://github.com/gonka-ai/gonka/pull/1255), [#1278](https://github.com/gonka-ai/gonka/pull/1278), [#1307](https://github.com/gonka-ai/gonka/pull/1307), [#1383](https://github.com/gonka-ai/gonka/pull/1383), [#1413](https://github.com/gonka-ai/gonka/pull/1413) by @0xMayoor, @0xgonka, @ouicate, @redstartechno, @GLiberman, @DimaOrekhovPS.

### devshard

- Serve inference during the PoC validation phase on validation-inference-capable nodes [#1348](https://github.com/gonka-ai/gonka/pull/1348) by @qdanik, @gmorgachev.
- Enable standalone `devshardd` to continue serving during DAPI outages using an ML-node cache, add per-escrow SQLite/Postgres storage routing, and snapshot `validation_rate` at escrow creation [#1417](https://github.com/gonka-ai/gonka/pull/1417) by @akup.
- Fix a production host leak where failed session resolution left validation workers alive [#1417](https://github.com/gonka-ai/gonka/pull/1417) by @gmorgachev.
- Collect gateway v3 fixes: runtime versioning, observability, escrow rotation, request parameter validation, SSE handling, drain barriers, and gateway v2 cleanup [#1427](https://github.com/gonka-ai/gonka/pull/1427) by @gmorgachev, @qdanik, @libermans, @a-kuprin.
- Distribute unsettled devshard escrow by slot instead of unique address [#1347](https://github.com/gonka-ai/gonka/pull/1347) by @0xMayoor.

## Testing

The devshard v3 runtime was deployed and verified first. During testing, `node` and `api` containers were stopped while devshard traffic was running; active requests were allowed to finish, and new requests were successfully created and executed through `/devshard/v3`.

After devshard v3 validation, the mainnet-style upgrade from `v0.2.13` to `v0.2.14` was tested.

## Contributors (sorted alphabetically)

- @0xgonka
- @0xMayoor
- @a-kuprin
- @akup
- @alancapex
- @d-bogdan-engenious (testing)
- @DimaOrekhovPS
- @GLiberman
- @gmorgachev
- @libermans
- @maria-mitina (testing)
- @ouicate
- @patimen
- @qdanik
- @redstartechno
- @Ryanchen911
