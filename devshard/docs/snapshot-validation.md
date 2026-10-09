# Snapshot validation

The goal is to prove that some `State(M)`, with `N <= M <= T`, is confirmed by
quorum. Ordinary owner-signature and transition checks on every supplied diff
are prerequisites for this proof.

Host signatures may be at different nonces. The verifier and gateway use the
rule below.

## Definitions

```text
S_N      = supplied State(N)
S_i      = Apply(S_(i-1), Diff(i)),  N < i <= T
r_i      = Root(S_i)
Diff(i)  = escrow-owner-signed transition from nonce i-1 to i
Sig_h(i) = host owner h's signature over (escrow, i, r_i)
w(h)     = h's trusted slot weight
Q        = floor(2 * total slots / 3) + 1
```

`QuorumConfirmed(M)` means there is a set of owners of total weight at least Q,
each with a valid supplied signature at some nonce n >= M, such that every
honest owner in that set accepted a protocol prefix containing `S_M` before
or at signing. An owner need not currently hold `S_M` or have signed at M.
Byzantine attestations count toward Q. Their actual acceptance is not asserted.

State equality means equality of protocol-relevant state, including everything
needed for validation and future transitions, not identical snapshot bytes or
diagnostic caches.

## Assumptions

1. Signatures are unforgeable and roots computationally bind protocol state.
   Byzantine slot weight is below Q, so every quorum contains an honest owner.
2. Honest hosts and the verifier share the trusted escrow, version, group,
   configuration and validation inputs. Transitions are deterministic.
3. The escrow owner authorizes only one diff per nonce. Every honest host
   signing at nonce n has accepted the common protocol prefix through n from
   the same trusted start and signs only the root of its accepted state at n.
   An honest host accepts a diff only if ordinary transition checks pass,
   including equality of the computed root and the signed `PostStateRoot`.
   This includes hosts using previously proved imports:
   prefix acceptance is a model assumption, not a claim that each host replayed
   or retained every earlier state.

Owner equivocation is outside this model. Additional equivocation handling is
outside this proposal.

> The long-term rule is to stop signing for an escrow as soon as a host verifies
> two conflicting owner-signed diffs for the same escrow and nonce. The host
> propagates both signed diffs to the other hosts as proof of owner equivocation.
> Hosts that verify the proof also stop signing and propagate it. The proof is
> intended to support a protocol penalty against the dishonest owner. The penalty
> and its enforcement are not specified here. This behavior is planned, not
> implemented.

## 1. Confirm a checkpoint

Replay in temporary state. For every supplied host signature, verify its nonce,
slot owner, signing-key authorization and signature against `(escrow, i, r_i)`.
Reject invalid signatures or nonces outside `[N,T]`.

Check two sets of owners:

```text
AtN    = {h | a valid supplied Sig_h(N) matches r_N}
AfterN = {h | a valid supplied Sig_h(i) matches r_i for some i in [N+1,T]}

CheckpointProof = sum[h in AtN] w(h) >= Q
                  OR sum[h in AfterN] w(h) >= Q
```

The first case proves `QuorumConfirmed(N)`. The second proves
`QuorumConfirmed(N+1)`, using the owner-signed post-state root in `Diff(N+1)`.
When T = N, `AfterN` is empty, so only the first case applies. For M > N,
the set of owners signing in `[M,T]` can only shrink as M increases. Checking
N+1 is therefore sufficient for acceptance. No checkpoint search is needed.

Count each owner once within each set, using its full trusted slot weight.
An owner may appear in both sets. Either set must reach Q on its own. Do not
combine their weights. Signatures after N may be at different nonces.

## 2. Validate every diff

For every i in `N+1..T`, including diffs used by the checkpoint proof:

```text
DiffAccepted(i) = owner signature verifies
                     (escrow, i, transactions, PostStateRoot)
                 AND Apply(S_(i-1), Diff(i)) passes ordinary transaction checks
                 AND r_i = Diff(i).PostStateRoot
```

Transaction checks include any required host signatures. They do not require
host quorum on every resulting state.

## Combined rule

Check trusted bindings, snapshot nonce N, `0 <= T-N <= 1000`, and contiguous diffs.
All replay states must remain Active with finalize nonce zero.

```text
AcceptPackage = all binding, bounds and supplied-signature checks pass
                AND for all i in [N+1,T]: DiffAccepted(i)
                AND CheckpointProof
```

After these checks and the existing target checks, install only `S_T`.
An executor already connected to the tail uses ordinary diff synchronization.

## Proof

Let `C_i` be the state at nonce i in the common authenticated history.
Assume the binding, signature and diff checks pass and `CheckpointProof` holds.

If `AtN` carries quorum weight, every honest owner in that set signed the
matching root at N. Each therefore accepted `S_N` under the binding assumption.
The set carries at least Q weight, so `QuorumConfirmed(N)` holds.

Otherwise, `AfterN` carries quorum weight and N < T:

1. The set contains an honest signer at some n >= N+1. Its accepted common
   prefix contains the transition at N+1.
2. The owner authorizes only one diff per nonce, so the supplied `Diff(N+1)`
   is that transition. By the acceptance checks in assumption 3, its signed
   `PostStateRoot` equals `Root(C_(N+1))`.
3. Replay checks `r_(N+1) = Diff(N+1).PostStateRoot`. Root binding therefore
   gives `S_(N+1) = C_(N+1)`.
4. Every honest owner in `AfterN` accepted the common prefix through its signed
   nonce n >= N+1, containing `C_(N+1)` and hence `S_(N+1)`.
5. The set carries at least Q weight, so `QuorumConfirmed(N+1)` holds.

Thus the package proves a common checkpoint under the stated assumptions,
except with negligible probability of a signature forgery or root-binding failure.
Ordinary validated diffs extend that checkpoint to T.

## Limits

The conclusion is quorum confirmation of M, not necessarily N or T. It relies
on the common-history and prefix-acceptance assumptions above. Without them,
later root signatures need not prove acceptance of earlier states.
