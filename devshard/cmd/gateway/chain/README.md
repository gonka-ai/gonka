# `chain` — all blockchain input and output

Every read from the network and every transaction the gateway signs passes through here. No other package talks to the chain.

## What it owns

- **The transaction client** (`txclient.go`, `tx_build.go`, `protoencode.go`, `grpc.go`) — build, sign, broadcast, confirm. A transaction is not assumed to have landed because it was accepted.
- **The phase observer** (`observer.go`, `observer_fetch.go`, `observer_preservation.go`, `observer_publish.go`, `observer_health.go`, `snapshot.go`) — polls the network's public API and publishes an immutable `PhaseSnapshot`: the epoch, its phase, which participants are preserved through proof-of-compute, the host weights the capacity model scales by, and the context window governance gives each model.
- **Escrow queries and settlement encoding** (`escrow_query.go`, `settlement.go`).
- **Protocol versions** (`versions.go`) — the gateway serves exactly one, fixed at build time.

## Boundaries

- **The snapshot is immutable and published whole.** A reader never sees half an update, and never has to lock.
- **A wrong chain id invalidates every signature**, so it is validated at startup rather than discovered on the first broadcast.
- **Weights can be absent.** A model whose weights the chain has not reported falls back to membership share, and says so, rather than scoring as zero.
- **The settle broadcast is narrated, not logged.** `Config.Narrator` hears the transaction hash as soon as the node accepts it, before the commit wait that can still fail; the journal writes `settle tx broadcast`.

## What the chain observer provides

`observer.go` polls the network's public API on a fixed interval, folds the raw inputs into a `PhaseSnapshot`, and publishes it whole. It derives nothing beyond that fold — no scoring, no policy — so every consumer reads the same numbers.

A failed poll **republishes the previous snapshot with `LastError` set** rather than an empty one: a network hiccup must not read as "the epoch has no participants". `LastUpdatedAt` is how a reader tells fresh data from a held-over view.

Six fields have absent values that are load-bearing, and each fails in a chosen direction:

| Field | Absent means | Read as |
| --- | --- | --- |
| `RequestsBlocked` | derived, never absent — `false` outside every PoC phase (`rawPoCBlockingState`) | requests admitted |
| `EffectiveEpochIndex` | the reply lacks the latest epoch's PoC start or its `set_new_validators` height, so `deriveEffectiveEpochIndex` cannot tell whether the chain switched; otherwise it is the latest epoch less one from the latest epoch's PoC start until its `set_new_validators`, and the latest epoch after | the latest epoch, the upper bound: every escrow deadline is then read early, never late (`escrow`, `effectiveEpoch`) |
| `Preserved` / `PreservedByModel` | the chain holds no snapshot for this episode | **everyone** is preserved (`scheduler`, `pocPreserved`) — fail open, so a missing snapshot never empties routing |
| `MaxNonce` | never observed: the chain has not enabled devshard escrow params, or no read has succeeded since start | the nonce gate falls back to `fallbackNonceCeiling` rather than to "no ceiling", and routing reports no escrow exhausted against it (`scheduler`, `reportExhausted`) |
| `TokenPrice` / `FeePerNonce` / `CreateDevshardFee` | never observed, for the same reasons as `MaxNonce`; all four come from one read of the chain's devshard escrow params (`grpc.go`, `EscrowParams`) | the escrow manager creates an escrow without checking its amount against the model's floor (`escrow`, `createFor`) — fail open, so a missing price never stops rotation |
| `Models` | no read has succeeded since start. A reply naming no model counts as no observation rather than as a chain that named nobody, and a failed read keeps the previous answer, so each model holds the length it was last priced at (`observer.go`, `PhaseObserver.fetchModels`) | a model no length has ever been observed or configured for is priced at `fallback_max_model_len` by the participant limiter's input window (`limits`, `ParticipantLimiter.windowsForLocked`), which the floor would then hold for the life of the process — hence the direction |

The effective epoch is derived, not queried. The public API's `/v1/epochs/latest` carries the latest epoch with its own stages but not the effective index; the chain's `GetCurrentEpoch` does, but over gRPC it would be answered by another node at another height, and a snapshot whose effective epoch ran one block ahead of its own switch height would read a deadline as already past. Deriving it from the one reply reads the latest epoch, the effective epoch and `EpochSwitchBlockHeight` at the same block, and the two derivations agree on when the switch happens: at exactly `set_new_validators` the switch has happened for both, so the effective epoch is the latest and `EpochSwitchBlockHeight` already names the next switch (`deriveEpochSwitchBlockHeight` takes the current epoch's `set_new_validators` only while it is strictly ahead of the block). Naming the switch just passed at that block would put the escrow manager inside the next bridge window with the new effective epoch, and it would fund a second temp set labelled one epoch further on.

Each model carries governance's `context_window` and the `--max-model-len` parsed from its `model_args` (`grpc.go`, `maxModelLenOf`; `NoMaxModelLen` when the args carry no plain number for it); the engine reads the second as the model's context length and falls back to the operator's pin only when the chain names no usable one (`config/config.go`, `Limits.ContextLength`), and the scheduler prices an escrow's retirement floor on the same length ([capacity.md](../docs/capacity.md), "The balance floor").

Weights follow the same rule: `CurrentWeightsByModel` is preferred, `CurrentWeights` is the fallback, and a model with neither scores by membership share rather than as zero.

`versions.go` fetches per-node protocol versions with `versionsPollConcurrency` = 16 workers and a `versionsFetchTimeout` of 2 s each, which keeps one full pass inside the poll's freshness window even on a large network.

### The poll loop

`Start` spawns the snapshot poll loop and the versions poller on a context derived from the caller's, and returns immediately; it is idempotent, so a second call is a no-op rather than a second set of pollers. `Stop` cancels that context and blocks until both loops have exited, and is a no-op before `Start`. Its done channel outlives the cancel, so a second concurrent `Stop` waits for the pollers to exit instead of returning while they still run.

`Subscribe` registers a callback invoked synchronously on every publish, mirroring `config.Holder`'s semantics: store before notify, cancel by deleting from the map. Subscribers are notified in no particular order.

A poll that fails only in part still publishes: `LastError` keeps **every** failed read of that poll, joined by `; `. Overwriting rather than joining shows only the last failure, leaving a frozen `MaxNonce` — the ceiling nonces are issued against — went unnamed. The observer decides when its health turned — degraded, then recovered — and narrates only that turn through `SetNarrator` to the journal, which writes `chain snapshot stale` and `chain snapshot recovered`, so a five-second poll speaks once instead of every tick; `LastError` carries the cause the health gauge cannot, by naming which read failed. `publish` narrates before it notifies subscribers, so a snapshot's health line comes before its epoch line.

### Which nodes count as preserved

`resolvePreservation` picks one of three rules per poll (`preservationModeAll`, `preservationModeSnapshot`, `preservationModeLegacy`):

| Rule | When | What it keeps |
| --- | --- | --- |
| all | outside PoC and outside confirmation PoC, and during the confirmation-PoC grace period | every node |
| snapshot | the chain holds a preserved-nodes snapshot whose episode anchor matches | the nodes that snapshot lists |
| legacy | anything else, a failed read included | nodes whose second timeslot allocation is set |

The anchor a snapshot must match is the confirmation-PoC trigger height or the epoch's PoC start height, depending on which reason blocked requests; zero skips the anchor check. During the confirmation-PoC grace period the matching snapshot intentionally does not exist yet, which is why that case falls to *all* rather than to *legacy*.

A chain that answers "no snapshot for this episode" is a different thing from one that could not be reached, and only the second is an error — a non-fatal one: the preserved-set view falls back to the legacy rule for that poll, and the failure is recorded in `LastError`.

### The serving set narrows two blocks before a PoC

The chain announces the next PoC's start height (`epoch_stages.next_poc_start`, the current PoC start plus one epoch length). From two blocks before it (`pocLeadBlocks`) the observer resolves the preserved set as if that PoC had begun, anchored at the coming start height (`preservationEpoch`): a poll every 5 s and a block about as long mean the last poll before the start can land up to two blocks early, and a request routed to a host on that poll would reach it already generating and come back empty. Only the serving set looks ahead. `RequestsBlocked`, `BlockReason` and `EpochPhase` stay with the chain's phase, so the planner, the capacity view, the race and the PoC mode keep reading the phase the chain is in. A confirmation PoC is triggered by an event, not a known height, so it is not anticipated; its grace period is the chain's own lead.

### PoC validation rejoins capable miners

During PoC validation the preserved and current views are rebuilt by `mergePreservedWithValidationCapable`: preserved miners keep their current weight, and each excluded miner with validation-inference-capable nodes is added back with the summed weight of exactly those nodes, per model. The input state is not mutated. Capability is fail-closed, so a cold versions cache keeps the merge conservative rather than admitting miners it has not confirmed.

`VersionsCache` is where that capability comes from. It polls each candidate miner's dapi `/v1/versions` — the participant's `inference_url` plus that path — and reports per-node PoC-validation-inference capability; unknown, errored and stale all read as false. Those URLs are participant-controlled, so the poll goes through its own client carrying the `common/httpguard` dial-time SSRF guard and, like the signed host transport, no proxy, which would otherwise be the only address the guard sees; the observer's chain API client stays unguarded so a private self-hosted API keeps working. The poller runs on its own interval, `DefaultVersionsPollInterval` (15 s, `GATEWAY_CHAIN_VERSIONS_POLL_INTERVAL_SECONDS`), not the observer's 5 s chain poll, because every candidate host answers each pass and a host may throttle that route. It polls every candidate every interval, and polls a miner right away rather than at the next tick when `SetCandidates` adds it or moves it to a new URL, which covers the whole first candidate set; miners already in the set at the same URL are not polled again on a candidate change. A moved miner's entry is dropped with its old URL. Only a successful fetch replaces a miner's entry: a failed, throttled or aborted fetch keeps the last good answer, which expires at `versionsTTLPollMultiplier` (3) times the versions interval (45 s by default), so a missed poll does not drop a capable node; a miner with no successful fetch yet has no entry and reads as not capable. An answer that arrives while its miner is no longer a candidate at the URL it was fetched from is dropped. A reply is read up to `versionsResponseMaxBytes` (1 MiB) and decoded through `internal/strictjson`, so a reply cut at the limit or malformed reads as not capable, like any other failed fetch.

### Folding the participants response

`parseParticipants` keys everything by gonka address and folds each participant into weights (current and full, flat and per model), the preserved/excluded partition, inference URLs, and the flattened (model, node, weight) triples the validation-capable merge needs. `models` and `ml_nodes` arrive as parallel arrays and are indexed in step. Full weights are always computed under the *all* rule, so the preserved rule of the moment changes the current view only. Preserved lists are sorted, so what a snapshot holds never depends on map iteration order.

The chain's numeric fields arrive as either JSON numbers or numeric strings, and the confirmation-PoC phase as either its enum name or its protobuf number (0–4); `jsonInt64`, `jsonUint64` and `confirmationPoCPhaseValue` accept both forms.

The epoch switch height is derived rather than read: the current epoch's `set_new_validators` while it is still ahead of the current block, else the next epoch's, else the current epoch's `next_poc_start`, else the latest epoch's PoC start height.

- **Each poll is bounded at six poll intervals.** The process context carries cancellation but no deadline, so a hung read would otherwise hold the loop for as long as the call takes.
- **The snapshot carries two clocks.** `LastUpdatedAt` moves on every publish and feeds the age gauge. `LastHealthyAt` moves only when the epoch and participant reads both landed, is carried forward otherwise, and is what the staleness gate judges. The nonce-ceiling and preserved-set reads fall back within the poll and do not hold it back.
- **`DefaultObserverPollInterval` is exported** because `Config.Validate` rejects a staleness limit below it. A limit under the refresh cadence refuses traffic from a healthy chain.

## Transaction encoding

Transactions are **unordered**: instead of a sequence number they carry a `timeout_timestamp` set `UnorderedTxTTL` (9 min) ahead of now. Inside that window a broadcast transaction can still land, so "not found" is never proof it failed — every reconciliation in [`escrow/`](../escrow/) waits out the window before concluding anything.

The `Default*` constants in `txclient.go` (fee denom and amount, gas limit, poll interval and poll timeout) are what `NewTxClient` applies to zero-value fields, and the single source `config.Defaults` reads. `EscrowDenom` (`ngonka`) is the denom `CreateDevshardEscrow` locks the amount in, and the default fee denom.

The three lookups mean three different things, and callers depend on the distinction:

| Call | `found`/`succeeded` false | `ErrTxNotFound` | error |
| --- | --- | --- | --- |
| `GetTxEscrowID` | committed but failed: no escrow will ever exist | the chain does not have it | transport failure — conclude nothing |
| `TxCommitted` | committed and rejected | the chain does not have it | transport failure — conclude nothing |
| `awaitTx` | — | a 404 here is "not indexed yet", not terminal | — |

Reading a transport failure as absence would rebuild and rebroadcast a settlement that already moved the money, so it is always returned as an error.

Both money-moving transactions take an `onPrepared` hook that records the precomputed hash **before** the irreversible broadcast; an error from it aborts the broadcast. They differ afterwards: `CreateEscrow` returns once the transaction is accepted, because its commitment row is reconciled later, while `SettleEscrow` waits for the commit — the caller destroys the means to retry, so the result has to mean "settled" and not "sent". A broadcast that is not confirmed in time returns `errUnconfirmed`, whose message says what the caller must not do: create another.

`awaitTx` is where a `DeliverTx` failure is learned at all: broadcasting in `BROADCAST_MODE_SYNC` reports `CheckTx` alone, so a transaction can be accepted and still never execute. It polls until a committed transaction satisfies the caller's readiness predicate, `pollTimeout` elapses, or the context is done, and returns a non-zero code as an error.

A non-zero `CheckTx` code from `Broadcast` is `ErrTxRejected`: the node refused the transaction, so it entered no mempool and can never land, and the hash `onPrepared` recorded names nothing on its way. The one exception is `tx already in mempool` (`sdk` code 19), which says the transaction is on its way; it stays a plain error, as does a transport failure, from which nothing about the transaction can be concluded.

### How a message is encoded

The two escrow messages are marshalled by the types generated from the chain's own `.proto`, so their wire layout tracks the chain by construction. Hand-laying those fields puts a money transaction's layout in a third place, edited in lockstep with the proto, or the settlement mis-encodes silently. `Marshal` cannot fail for these two messages — neither carries an `Any` nor a custom type — and the error is returned rather than dropped so a future field cannot make it silent.

Everything around the message is hand-encoded in `protoencode.go`: the `Any` wrapper, the secp256k1 pubkey, the `Fee`, a `SignerInfo` with a single `SIGN_MODE_DIRECT` mode, the `AuthInfo`, the `SignDoc` that gets hashed and signed, and the broadcastable `TxRaw`. The body is an unordered `TxBody` — field 1 the message, field 4 `unordered=true`, field 5 the `timeout_timestamp`. The tx hash is the upper-hex SHA-256 of the tx bytes, which is what lets it be recorded before the broadcast and compared against the hash the node returns.

The signature is truncated to `r||s`, 64 bytes: the recovery byte the signer produces does not go into the tx. The account sequence is signed as zero for an unordered transaction, so only the account number reaches the wire. The TTL is passed in by the caller rather than read from the clock inside the builder, which keeps encoding deterministic.

## The gRPC transport

`GRPCChain` answers both `Reader` and `Transport` over one connection: the gateway dials the chain once, for the escrow bridge, and every other chain read rides that same connection and its query fallback. Both are interfaces rather than the client itself, which is what lets tests answer them without a connection and keeps the gateway's own tests off the network.

`ChainID` asks the node unless a value was configured; a configured value is taken as the operator's decision and wins over what the node reports, because a mismatch invalidates every signature.

`SpendableBalance` reads one denom through `x/bank`'s `SpendableBalanceByDenom`, which leaves out vesting coins still locked; `CreateEscrow` calls it for the signer before it prepares anything and refuses with `WalletUnderfundedError` (`ErrWalletUnderfunded`) when the wallet holds less than the amount plus the fee, the fee counted only when it is paid in `EscrowDenom`. A read that fails stops the create too: an unknown balance is not permission to broadcast.

`Account` reads the number and sequence through the account's own registered type, so a vesting or module account answers correctly instead of by whichever nested field a search happened to reach first.

Absence and failure are kept apart in every query that can return neither:

| Query | `found=false` means | An error means |
| --- | --- | --- |
| `Tx` | the node has not indexed the transaction yet, which a caller polling after a broadcast must tell apart from a failure | the query itself failed |
| `Escrow` | the chain says the escrow is absent | the query failed — reading it as absence would retire an escrow that still holds funds, stranding them |
| `EscrowParams` | the chain carries no devshard escrow params, i.e. it has not enabled them | the query failed |
| `PreservedNodes` | the chain holds no snapshot for the current episode, which routing reads as "no preserved set" rather than as an empty one | the query failed |

`isNotFoundStatus` matches the gRPC `NotFound` code and, as a fallback, the same text in the message, because some nodes answer `Unknown` with that text rather than the typed code.
