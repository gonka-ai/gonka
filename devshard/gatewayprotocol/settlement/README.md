# `settlement` — the settle message, its signatures and its root

## What it owns

- `Message`, `HostStat` and `SlotSignature`: the settle payload with its binary fields decoded, one field for each field of `MsgSettleDevshardEscrow` apart from the settler, which the signer supplies. `HostStat` also carries `Validated` and `Finished`, the counters of fields 7 and 8 of a host-stats entry that devshardctl posts and that `state` puts in the root; the chain type in this module has no such fields, so `settlementwire` does not map them.
- `Root`: the settlement state root, `state.ComputeHostStatsHash` over the payload's host stats followed by `state.ComputeStateRootFromRestHash` in `types.PhaseSettlement` with the payload's rest hash, fees and version. It hashes the host stats exactly as given and never filters them, so a caller gets the root of the map it passed.
- `PresentStats`: a copy of a host-stats map without its nil entries.
- `HostStatsBySlot` and `SignaturesBySlot`: the two lists of the message in ascending slot order. `HostStatsBySlot` expects every entry to be non-nil.
- `ChooseSignatures` over a `SignatureSource`, which `*user.Session` satisfies: the signatures a settle posts, as `Signatures`. It posts the session's re-checked set (`SettlementSignatures`) when the re-check passes. When the re-check errors, it posts every signature the session holds at the nonce (`SignaturesAt`) when they cover at least the chain's quorum of slots, more than two thirds of them, and reports the shortfall beside them in `Shortfall`; below that it returns an error wrapping the shortfall and the settle posts nothing.

## How the gateways use it

Both gateways take the signatures of their payload from `ChooseSignatures`, with the escrow's slot count from the snapshot's group; `devshard/cmd/gateway` narrates the shortfall of a set posted unchecked and devshardctl logs it. `devshard/cmd/gateway` drops nil entries with `PresentStats` first, then takes the root, the host stats and the signatures of that copy; the chain recomputes the root over the host stats the transaction carries, so both must come from the same map. `devshard/cmd/devshardctl` takes the root of its payload as built, whose entries are non-nil by construction in `devshard/state`, and lists its host stats and signatures with the two `BySlot` functions, so both gateways send both lists in ascending slot order.

What a settle posts short of a checked quorum is protocol too. The chain counts one vote per slot signature in the message, needs more than two thirds of the escrow's slots, and accepts a slot's signature by its address or by any key that address has granted, looked up when the settle runs; one signature it cannot accept fails the whole message. A failed settle is a fee-exempt transaction that leaves the escrow as it was, while an escrow never settled is paid out whole to its group when it is pruned and its creator gets no refund. Every signature a session holds was checked when it arrived and stored under every slot of its validator; the re-check fails mostly for a warm key the session cannot resolve now, which a read-only session without a resolver cannot do at all and the chain still accepts. Any error of the re-check takes the same path, a missing local root for the nonce included, since the chain recomputes the root from the message. So both gateways post the held set when it can reach the chain's count and post nothing when it cannot, since that settle could only fail. A held set at the count that still holds a signature the chain refuses fails on chain and is retried; it cannot be told apart locally from a warm key with a grant.

The order of the host stats is protocol: the chain gives every host-stats entry its share of the fees and the remainder of the fees over the slot count one coin per entry in list order, so a fixed order is what makes the same payload pay the same validators every time either gateway builds it. The order of the signatures does not change what the chain accepts or pays, since it checks each signature on its own, refuses duplicates and counts the votes; both gateways sort them as well, so the same payload always yields the same settle message.

## What it does not own

Escrow-id parsing, collecting signatures, narrating a settlement the chain would refuse, the devshardctl admin JSON, and the wire encoding, which is [`settlementwire`](../settlementwire/README.md).

## Imports

`devshard/state`, `devshard/types` and the standard library.
