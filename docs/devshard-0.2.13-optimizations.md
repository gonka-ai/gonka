# Devshard 0.2.13 optimizations

Brief overview of devshard-related changes on the `ak/devshard-0.2.13-optimizations2` branch (target: `devshard-0.2.13-v2`). This PR adds **two commits** on top of the base branch; runtime long-poll, epoch prune, and related dapi work are already there.

## Version naming (`c2c470940`)

Devshard uses **two independent version concepts**. Do not mix them.

| Name | Source | Purpose |
|------|--------|---------|
| **Runtime / binary version** | `DevshardEscrowParams.approved_versions` | Which devshard binary may run. versiond sets `DEVSHARD_LOG_PREFIX` and `DEVSHARD_BINARY_VERSION` to the oracle name; devshardd checks it against its build. |
| **State root & protocol version** | `types.DevshardStateRootAndProtocolVersion` (`"v2"`) | Tag hashed into state roots and carried on `MsgSettleDevshardEscrow.state_root_and_protocol_version`. Bump only when root composition or settlement rules change. |
| **Legacy route tag** | `types.LegacyRouteSessionVersion` (`"v1"`) | Storage bind tag for `/v1/devshard` and embedded dapi hosts only. |

Details: [devshard/docs/protocol-version.md](../devshard/docs/protocol-version.md), [devshard/docs/upgrade.md](../devshard/docs/upgrade.md).

## Session workload limit (`37f1177c4`)

Chain parameter **`DevshardEscrowParams.max_nonce`** caps the highest settlement nonce for a devshard escrow. Hosts enforce it before applying diffs (with a reserve for finalization/settlement via `MaxActiveNonce`). There is **no** separate `max_inferences_per_devshard` governance field; nonce limits bound inference IDs in the devshard protocol.

`MaxNonceProvider` is wired from dapi (`ConfigManagerMaxNonce`) and devshardd (`RuntimeConfigMaxNonce` from long-poll). Hybrid storage `PruneEpoch` / `pruneBefore` / `Close` return combined SQLite and Postgres errors (`errors.Join`).

## Not in this PR (follow-up PRs)

**Database migration** and **stricter backend stickiness** are still outstanding:

- No automatic migration of SQLite-fallback sessions into Postgres when PG reconnects; hybrid routing stays sticky per escrow ([storage-design](../devshard/docs/storage-design.md)).
- Legacy SQLite → Postgres / epoch layout migration exists but is not fully hardened for all operator paths.
- Tighter enforcement that an escrow never straddles backends, clearer operator tooling, and migration runbooks are expected in **further PRs**.

## Commits

| Commit | Summary |
|--------|---------|
| `c2c470940` | Strict version naming: runtime/binary vs `StateRootAndProtocolVersion`; `DEVSHARD_BINARY_VERSION`; settlement proto `state_root_and_protocol_version`; docs and storage session-version guards. |
| `37f1177c4` | Enforce chain `max_nonce` on hosts before settlement; `MaxNonceProvider` from dapi/devshardd; hybrid prune `errors.Join`. |

Other commits should be rebased after other branches will be merged
