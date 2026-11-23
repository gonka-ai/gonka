# `settlementwire` — the settle message on the wire

## What it owns

`Marshal` maps a `settlement.Message` and the settler's address onto the chain's generated `MsgSettleDevshardEscrow` and marshals it. The generated type tracks the chain's `.proto` by construction, so no field is laid out by hand. The generated type omits zero-valued scalars, as protobuf does; the chain decodes a message with them written or omitted to the same value. `Marshal` cannot fail for this message, which carries neither an `Any` nor a custom type, and the error is returned rather than dropped so a future field cannot make it silent.

## What it does not own

The transaction around the message (the `Any` wrapper, body, auth info, sign doc), signing and broadcasting stay in `devshard/cmd/gateway/chain`; the create-escrow message too. `devshard/cmd/devshardctl` builds its settle transaction through `common/chain/tx` and calls this package only from tests. Its `settle_wire_equivalence_test.go` sends the full, empty-optional and zero-scalar messages as settlement JSON through `settleParamsFromJSON` and `common/chain/tx` to the in-process mock chain, decodes the broadcast `MsgSettleDevshardEscrow` and asserts it equals `Marshal`'s output decoded the same way. The raw bytes are equal for the full and empty-optional messages; for the zero-scalar message they differ, because `common/chain/tx` writes the zero `slot_id` and zero counters of a host-stats entry and the zero `slot_id` of a slot signature that `Marshal` omits.

## The goldens

`testdata/` holds five base64 goldens: three whole messages (`settle_msg_full`, `settle_msg_empty_optional`, and `settle_msg_zero_scalars`, whose slot 0 and counters are zero) checked through `Marshal`, and one host-stats entry and one slot-signature entry checked through the package's mappings, all from the fixed inputs `marshal_test.go` repeats. `settle_msg_zero_scalars` is `Marshal`'s own output for that message, not an independent encoding. No generator writes them, so a golden here changes only by hand and with a reason.

## Imports

`devshard/gatewayprotocol/settlement`, `github.com/productscience/inference/x/inference/types` and the standard library. It is the one package in the tree that imports chain types.
