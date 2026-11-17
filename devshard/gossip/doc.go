// Package gossip propagates nonce awareness, signatures, and transactions
// across devshard hosts. Two channels: nonce gossip (K random peers) and
// tx broadcast (all peers, deduplicated).
//
// Flows:
//
// AfterRequest: stores nonce in seen map, sends (nonce, hash, sig, slot) to K peers.
//
// OnNonceReceived: checks for equivocation (same nonce, different hash -> error).
// Marks SEEN, forwards to K peers. Tries to accumulate the signature via SigAccumulator.
// A nonce more than 4096 past lastAfterReqNonce is dropped: not stored, not
// counted in highest SEEN, not forwarded.
//
// BroadcastTxs: sends mempool txs to all peers. Each tx is sent at most once.
//
// Rebroadcast (every 30s): re-sends SEEN nonces older than StaleTTL to K peers.
//
// Recovery (every 60s): if highest SEEN > lastAfterReqNonce and no recent user
// contact, fetches diffs via DiffFetcher, applies via StateUpdater (which verifies
// user signatures), signs each nonce and gossips own sigs. One attempt covers at
// most 4096 nonces past lastAfterReqNonce, 8 windows per tick.
package gossip
