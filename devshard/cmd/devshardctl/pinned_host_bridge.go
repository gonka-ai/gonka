package main

import (
	"devshard/bridge"
)

// pinnedHostBridge wraps a devshard/bridge.MainnetBridge and overrides
// GetHostInfo so every address resolves to the same pinned URL. Every
// other method is forwarded to the inner bridge verbatim.
//
// This is the testenv `--host` / `DEVSHARDD_URL` implementation: in
// the compose network, every devshardd-testenv container is
// addressable by its service name (e.g. `http://devshard-1:9500`), so
// a developer driving escrow traffic at a specific host can pin the
// proxy without re-registering hosts on-chain. In prod, the REST
// bridge's per-participant URL registry handles host routing; no
// operator would normally pin.
//
// Thread safety: the wrapper is stateless — every field is read-only
// after construction — so concurrent calls are inherently safe.
type pinnedHostBridge struct {
	inner     bridge.MainnetBridge
	pinnedURL string
}

// newPinnedHostBridge returns a bridge that pins GetHostInfo to
// pinnedURL. Panics on an empty pinnedURL because the only caller
// (buildBridge) already gates on non-empty, so an empty URL here is a
// programmer error.
func newPinnedHostBridge(inner bridge.MainnetBridge, pinnedURL string) *pinnedHostBridge {
	if pinnedURL == "" {
		panic("devshardctl: pinned host URL must be non-empty")
	}
	return &pinnedHostBridge{inner: inner, pinnedURL: pinnedURL}
}

// GetHostInfo returns a HostInfo with Address==the requested address
// and URL==the pinned URL. Returning the original address (rather
// than forging it) keeps state-machine signature checks working: the
// auth middleware at the devshardd side looks up the caller by
// address, not by URL.
func (b *pinnedHostBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: b.pinnedURL}, nil
}

// Every other MainnetBridge method forwards unchanged.
func (b *pinnedHostBridge) GetEscrow(id string) (*bridge.EscrowInfo, error) {
	return b.inner.GetEscrow(id)
}
func (b *pinnedHostBridge) VerifyWarmKey(warm, validator string) (bool, error) {
	return b.inner.VerifyWarmKey(warm, validator)
}
func (b *pinnedHostBridge) OnEscrowCreated(e bridge.EscrowInfo) error {
	return b.inner.OnEscrowCreated(e)
}
func (b *pinnedHostBridge) OnSettlementProposed(id string, root []byte, nonce uint64) error {
	return b.inner.OnSettlementProposed(id, root, nonce)
}
func (b *pinnedHostBridge) OnSettlementFinalized(id string) error {
	return b.inner.OnSettlementFinalized(id)
}
func (b *pinnedHostBridge) SubmitDisputeState(id string, root []byte, nonce uint64, sigs map[uint32][]byte) error {
	return b.inner.SubmitDisputeState(id, root, nonce, sigs)
}

// Compile-time assertion that we implement the same shape as the
// wrapped bridge.
var _ bridge.MainnetBridge = (*pinnedHostBridge)(nil)
