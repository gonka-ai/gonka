// Package tx signs and broadcasts Cosmos transactions over gRPC (cosmos.tx.v1beta1).
//
// Gateway callers use UnorderedSigner (hex key) for create/settle escrow txs.
// devshardd callers use keyring-backed ordered signing for dispute settlement.
package tx
