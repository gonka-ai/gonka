# Host signature verification

Every host-authored message in devshard is authenticated the same way, and both
binaries (`devshardd` and `devshardctl`) run the same code to do it. This
document describes that shared structure, how to reuse it when adding a signed
message or endpoint, and how a warm key the host has never seen before gets
admitted.

## 1. A signature check has two independent halves

Keeping these separate is the whole design. Almost every historical bug in this
area came from conflating them.

**The preimage** is "what bytes were signed." This is necessarily
message-specific: a `MsgHeightAck` and a `MsgFinishInference` sign different
things. Each message type owns exactly one helper that builds its canonical
bytes, and both the signer and the verifier call that helper. Nobody
reconstructs the preimage by hand at a call site.

**The identity** is "who was allowed to produce that signature for this slot."
This is *not* message-specific, and it is the same rule for HeightAck, confirm,
finish, timeout and error-miss votes, repair requests and responses, gossip
state signatures, and settlement. It lives in one place:
`signing.SlotActors`.

The HeightAck bug that motivated this work was never a second crypto path. Both
paths used the same `ecrecover`. The ack path had its own hand-written identity
rule that accepted a cold key *or* a warm key depending on which branch you
entered, while confirm/finish had a different hand-written rule. Unifying the
identity half fixed it.

## 2. The preimage layer

Signature recovery itself is one library, `devshard/signing`: `Signer.Sign` and
`Verifier.RecoverAddress` over secp256k1. There is no second implementation.

Canonical bytes are built by these helpers, one per message family:

| Message | Canonical bytes | Domain tag |
|---|---|---|
| `MsgHeightAck` | `heightsync.CanonicalAckBytes` | `heightsync.ack.v1` |
| Anchor origin section | `heightsync.CanonicalOriginBytes` | `heightsync.origin.v1` |
| Repair request / response | `heightsync.CanonicalRepairRequestBytes` / `...ResponseBytes` | `heightsync.repair.v1` |
| `MsgValidation` | `types.CanonicalSignedBytes` | `devshard.validation.v1` |
| `MsgValidationVote` | `types.CanonicalSignedBytes` | `devshard.validationvote.v1` |
| Timeout / error-miss votes | `types.CanonicalSignedBytes` | `devshard.timeoutvote.v1` / `devshard.errormissvote.v1` |
| Finish proposer sig | `types.CanonicalSignedBytes` (no prefix) | (none; layout is unique among unprefixed signed protos) |
| Executor receipt | deterministic proto of `ExecutorReceiptContent` | (none) |
| State / gossip / settlement | deterministic proto of `StateSignatureContent` | (none) |
| User diffs | deterministic proto of `DiffContent` | (none) |

Protobuf does not encode the message type name. Two messages with the same field numbers and wire types marshal to identical bytes, so a signature over one verifies as the other. `types.CanonicalSignedBytes` prefixes a unique domain tag for families that share a layout (`MsgValidation` / `MsgValidationVote`) or a proto3-omissible prefix (`TimeoutVoteContent` / `ErrorMissVoteContent`). Height-sync messages already had tags.

CI enforces this: `TestSignedProtoPreimagesAreNotInterchangeable` walks every `proto/devshard/v1` message, requires it to be classified as signed or not, and fails if two signed preimages in the same domain have equal or subset field layouts. `make test` runs it; the unit-test workflow also has a named step so a collision shows up as its own check.

Two rules apply to all of them:

- **Domain separation.** A blob signed for one purpose must not verify for
  another. Height-sync messages and overlapping vote/validation protos prefix a
  version-tagged domain string. Unprefixed signed protos must keep disjoint
  field layouts (enforced by the unit test above).
- **Deterministic marshal.** Every proto preimage uses
  `proto.MarshalOptions{Deterministic: true}` (via `types.CanonicalSignedBytes`
  or the heightsync helpers). For messages that are all scalars today this
  happens to match the default encoding, but relying on that is a trap: adding
  a single map field to the proto would silently make the preimage
  non-reproducible and split verification across nodes. Use the option
  unconditionally.

## 3. The identity layer: `signing.SlotActors`

```go
type SlotActors struct {
	SlotKeys   map[uint32]string // slot -> validator (cold) address
	WarmKeys   map[uint32]string // slot -> bound warm address, from escrow state
	AcceptWarm func(slotID uint32, recovered, expected string) bool
}

func (a SlotActors) Allows(slotID uint32, recovered string) bool
```

`Allows` answers one question: may `recovered` act for `slotID`? It checks, in
order:

1. **Cold key.** `recovered == SlotKeys[slotID]`. A validator can always act for
   its own slot, whatever else is bound.
2. **This slot's warm binding.** If `WarmKeys[slotID]` is set, that binding is
   *exclusive* — only it or the cold key is accepted, and the check stops here.
3. **A sibling slot's warm binding**, reached only when the slot is still
   unbound. If the same warm address is bound on another slot owned by the same
   validator, accept.
4. **`AcceptWarm`**, the live authz check, also reached only when the slot is
   unbound. Nil means "decide from state only."

### Why the sibling rule is sound

An authz grant on chain is `(granter = validator address, grantee = warm
address)`. It is per-address, not per-slot. So if warm key `W` is bound on slot
0 and the validator that owns slot 0 also owns slot 1, the chain has *already*
said `W` may act for that validator. Reusing the binding is not a privilege
widening; it is reading a fact we already checked. Settlement weight is
unaffected because signature dedup there is still keyed by cold address, so a
sibling-accepted signature cannot double-count.

### The determinism boundary

This matters more than it looks. Steps 1 through 3 are decided from consensus
state alone (`SlotKeys` plus `WarmKeys`), so every replica applying the same
diff from the same prior state answers identically, with no network call.
`AcceptWarm` is the *only* step that can consult the chain, and it is reached
only for a slot with no binding yet.

Keep it that way. If you make an earlier step call out to the bridge, two
replicas whose bridges disagree — or one of which is briefly unreachable — will
seal different rest hashes and fork the escrow.

## 4. Where each caller gets its actor set

Nobody constructs `SlotActors` inline at a call site. There is one constructor
per context, and which one you want is decided by whether you may mutate escrow
state:

| Context | Constructor | `AcceptWarm` behavior |
|---|---|---|
| State machine, apply path | `sm.hostSignerAllowedLocked(slot, recovered)` | `ResolveWarmKey` — resolves **and caches** the binding into state |
| State machine, read-only verification | `sm.hostSignerCachedLocked(slot, recovered)` | nil — state only, never calls out, never mutates |
| Host and transport (RPC handlers) | `host.SlotActors()` | `CheckWarmKey` — resolves without caching |
| Log-plane L2 | `LogPlaneState.actors()` | `CheckWarmKey` |

Only the apply path binds. Everything else is either pure state or a
non-caching lookup, because a handler or a verifier goroutine that wrote
`WarmKeys` would be mutating consensus state outside diff application.

Two exported state-machine helpers wrap this for callers that hold no lock.
Both check state under `RLock`, release it, and then make **at most one** bridge
call:

- `sm.HostSignerAllowed(slotID, recovered)` — when the message names a slot.
- `sm.HostSignerAllowedAddr(expected, recovered)` — when the message names a
  host address instead, e.g. a state signature attributed to a group member.

`host.SlotActors()` memoises its authz lookups for the lifetime of the returned
value. A handler that checks two identities for one request — the repair handler
checks both the request signer and the authenticated HTTP sender — should call
it **once** and reuse the value, or it pays for the same chain lookup twice. The
memo is not synchronised; treat the value as request-scoped and do not stash it
on a struct.

## 5. Adding a new signed message or endpoint

Do these in order. Steps 2 and 4 are where people go wrong.

1. **Add one canonical-bytes helper** for the new message, alongside the
   existing ones. Prefer `types.CanonicalSignedBytes`, which applies
   deterministic marshal and, when needed, a unique domain tag. If the new
   proto's field numbers and wire types overlap an existing signed message,
   it **must** get a unique domain — protobuf does not encode the type name.
   Classify the message in `signed_preimage_test.go` (signed vs unsigned);
   `TestSignedProtoPreimagesAreNotInterchangeable` will fail the unit-test
   workflow until the layout or domain is unique.

2. **Sign and verify through that same helper.** Never rebuild the preimage at
   the verification site. If signing zeroes a signature field before marshaling,
   verification must zero the same field, which is why it belongs in one
   function.

3. **Recover with `signing.Verifier`.** Do not add a second recovery path.

4. **Decide identity with `SlotActors.Allows`**, taking the actor set from the
   table in section 4 rather than building one inline. Concretely:

   ```go
   recovered, err := verifier.RecoverAddress(blob, sig)
   if err != nil {
       return err
   }
   actors := s.host.SlotActors() // once per request
   if !actors.Allows(msg.SlotId, recovered) {
       expected, _ := actors.Expected(msg.SlotId)
       return fmt.Errorf("signer %q not an actor for slot %d (cold %q)", recovered, msg.SlotId, expected)
   }
   ```

   Use `actors.Expected(slotID)` for the error message. Do not index
   `host.Group()[slotID]` directly — the map lookup inside `SlotActors` does not
   assume slot IDs are compact `0..n-1`.

5. **If the message names a host address rather than a slot**, use
   `sm.HostSignerAllowedAddr`. If you have already resolved the one address that
   may sign, `signing.Exact(slotID, key)` gives you a single-key actor set with
   the same interface.

6. **Do not write a new "is this address a warm key" helper.** That is the
   mistake `IsWarmKeyForSlot` represented: a parallel rule that drifted from the
   apply path. It has been removed.

Three things are deliberately *not* unified, and should stay that way:

- **HTTP admission** (`Server.isAllowedSender` / `isWarmKeySender`) answers "may
  this peer talk to me at all," which is a coarser question than "does this
  signature bind this slot." It is a cheap gate in front of the real check, not
  a replacement for it.
- **User diffs** are exact-match against the escrow owner. There is no slot and
  no warm key; `SlotActors` does not apply.
- **Request-leg marks** (`heightsync.VerifyRequestLegOffline`) recover the
  *user's* signature over an HTTP request body. Same `Verifier`, but the
  identity being established is the escrow owner, not a slot actor.

## 6. Warm keys the host has not seen yet

### Where a warm key comes from

A validator grants a warm key on chain via authz, for the
`/inference.inference.MsgStartInference` message type. Nothing in the escrow
creates a binding; devshard only ever *discovers* one. The group a session is
built from lists cold validator addresses only, so on a fresh session every slot
starts unbound and the first message from a warm host is by definition from an
address the host has never seen.

### The three ways an unknown key gets admitted

Given a signature that recovers to an address that is not the slot's cold key:

1. **`WarmKeys[slot]` already holds it.** Free; no network. This is the steady
   state after the first admission.
2. **A sibling slot of the same validator holds it.** Free; no network. This
   covers a multi-slot validator whose warm key was first seen on a different
   slot — common for HeightAck, because a heartbeat can name any slot the host
   owns, including one that has never executed an inference.
3. **Live authz lookup**, via `WarmKeyResolver`, wired to
   `Bridge.VerifyWarmKey`. This is the only path that can admit a genuinely new
   key, and it queries `GranteesByMessageType` for the slot's cold address and
   looks for the recovered address in the result.

### Bind versus check

There are two wrappers around the resolver, and the difference is whether the
result is written into escrow state:

- **`ResolveWarmKey(slot, recovered, expected)`** — on success, writes
  `state.WarmKeys[slot] = recovered`. Used on the apply path, where the binding
  becomes part of the state and must be reproducible on replay.
- **`CheckWarmKey(recovered, expected)`** — returns the answer and writes
  nothing. Used by RPC handlers, log-plane L2, and read-only verification, none
  of which may mutate escrow state.

Sibling admission binds nothing. It does not need to: the fact is already
derivable from state, so re-deriving it on replay is free and deterministic.
Writing a redundant binding would only add a bridge call and a way for two
replicas to disagree.

### How a new binding reaches the other hosts

Hosts do not each independently query the chain and hope to agree. The binding
travels with the diff:

1. The sequencer admits the warm signature during compose and `ResolveWarmKey`
   writes `WarmKeys[slot]`.
2. `ValidateDiff` returns the post-state `WarmAfter` map. The caller diffs it
   against the pre-state with `types.ComputeWarmKeyDelta` and stores the result
   as `DiffRecord.WarmKeyDelta` next to the diff.
3. On replay — host restart, `RecoverSession`, reconcile — `InjectWarmKeys`
   restores each record's delta before the diff is re-applied, so verification
   takes the cached path and needs no chain access. `InjectWarmKeys` never
   overwrites an existing binding.

This is what makes a chain-dependent decision safe to embed in consensus state:
it is queried once, then carried.

### Caching and timeouts

`ChainBridge.VerifyWarmKey` memoises `(warm, validator) -> bool` in a
`sync.Map` for the process lifetime, so a repeatedly-probed key costs one query.
Each query is bounded by a 10s timeout, because callers reach the resolver from
state-machine apply while holding session locks and an unresponsive node must
not stall the escrow.

During user-session recovery the resolver is deliberately stubbed out
(`deferredWarmKeyResolver` returns "not a warm key" until recovery completes),
so replay is driven entirely by stored `WarmKeyDelta` records rather than by
whatever the chain says right now.

### Known limitation: bindings are sticky

Once `WarmKeys[slot]` is set, it is never re-validated against the chain — step
2 of `Allows` short-circuits before `AcceptWarm`. Revoking an authz grant
therefore does not evict an existing binding for the life of the escrow. This is
pre-existing behavior, not a consequence of the unification; see
`docs/proposals/warm-key-revoke.md`.

## 7. Pitfalls worth repeating

- A bound slot is exclusive. Do not let a sibling binding or a live authz answer
  override a binding that is already in state.
- Do not introduce a bridge call before step 4 of `Allows`. Consensus decisions
  must be derivable from state.
- Do not call `host.SlotActors()` twice in one handler; the second call loses
  the memo and repeats the chain lookup.
- Do not copy the `WarmKeys` map to check one address. The state machine's
  helpers read it under `RLock` without copying.
- `ecrecover` dominates the cost of any verification by roughly three orders of
  magnitude over the identity check. Optimise the number of *bridge* calls, not
  the number of string comparisons.
- Protobuf does not encode the message type. A new signed proto with the same
  field numbers and wire types as an existing one is a signature-replay bug.
  Give it a unique domain tag in `CanonicalSignedBytes` (or disjoint fields)
  and classify it in `signed_preimage_test.go`.
