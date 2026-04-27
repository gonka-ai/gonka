// Package gossip propagates nonce awareness, signatures, and transactions
// across subnet hosts. Two channels: nonce gossip (K random peers) and
// tx broadcast (all peers, deduplicated).
//
// Flows:
//
// AfterRequest: stores nonce in seen map, sends (nonce, hash, sig, slot) to K peers.
//
// OnNonceReceived: checks for equivocation (same nonce, different hash -> error).
// Marks SEEN, forwards to K peers. Tries to accumulate the signature via SigAccumulator.
//
// BroadcastTxs: sends mempool txs to all peers. Each tx is sent at most once.
//
// Rebroadcast (every 30s): re-sends SEEN nonces older than StaleTTL to K peers.
//
// Recovery (every 60s): if highest SEEN > lastAfterReqNonce and no recent user
// contact, fetches diffs via DiffFetcher, applies via StateUpdater (which verifies
// user signatures), signs each nonce and gossips own sigs.
package gossip
