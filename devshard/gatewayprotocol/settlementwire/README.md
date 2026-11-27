# `settlementwire` — the settle message on the wire

## What it owns

`Marshal` maps a `settlement.Message` and the settler's address onto the chain's generated `MsgSettleDevshardEscrow` and marshals it. The generated type tracks the chain's `.proto` by construction, so only the host-stats field is laid out by hand: its tag and length, and fields 7 and 8 of each entry. The host-stats type in this module stops at field 6, while a chain whose host-stats message defines `validated = 7` and `finished = 8` rebuilds the settlement root from them too, so `Marshal` appends `Validated` as field 7 and `Finished` as field 8 to each host-stats entry, each only when non-zero, as `common/chain/tx` does. The message is written as the generated fields 1 to 5, each host-stats entry, then the generated fields 7 to 9, which keeps the fields in ascending order. The generated type omits zero-valued scalars, as protobuf does; the chain decodes a message with them written or omitted to the same value. `Marshal` cannot fail for this message, which carries neither an `Any` nor a custom type, and the error is returned rather than dropped so a future field cannot make it silent.

## What it does not own

The transaction around the message (the `Any` wrapper, body, auth info, sign doc), signing and broadcasting stay in `devshard/cmd/gateway/chain`; the create-escrow message too.

## The goldens

`testdata/` holds six base64 goldens: three whole messages (`settle_msg_full`, `settle_msg_empty_optional`, and `settle_msg_zero_scalars`, whose slot 0 and counters are zero) checked through `Marshal`, and two host-stats entries (`settlement_host_stats`, and `settlement_host_stats_validated_finished`, whose bytes were written out by hand from the field numbers) and one slot-signature entry checked through the package's mappings, all from the fixed inputs `marshal_test.go` repeats. `settle_msg_zero_scalars` is `Marshal`'s own output for that message, not an independent encoding. No generator writes them, so a golden here changes only by hand and with a reason.

## Imports

`devshard/gatewayprotocol/settlement`, `github.com/productscience/inference/x/inference/types`, `google.golang.org/protobuf/encoding/protowire` and the standard library. It is the one package in the tree that imports chain types.
