package transport

import "sync"

// Peer RPC path labels for gateway adoption metrics (finding 26).
const (
	PeerRPCPathJSON = "json"
	PeerRPCPathH2   = "h2"
)

// PeerRPCAdoptionSink is implemented by the gateway metrics registry.
// PeerConn (Phase 2) must not increment these on devshardd.
type PeerRPCAdoptionSink interface {
	IncEscrowSession(path string)
	SetHostRPC(peer, mode string, on bool)
}

// PeerRPCAdoption records which path an escrow uses to talk to a host.
// PeerConn is per (host, version); escrow_sessions_total counts escrow work
// on that slot, not Attaches. Two escrows sharing one ready PeerConn
// increment the counter twice and set the host gauge once.
type PeerRPCAdoption struct {
	mu    sync.Mutex
	ready map[string]bool
	bound map[string]struct{}
	sink  PeerRPCAdoptionSink
}

func NewPeerRPCAdoption(sink PeerRPCAdoptionSink) *PeerRPCAdoption {
	return &PeerRPCAdoption{
		ready: make(map[string]bool),
		bound: make(map[string]struct{}),
		sink:  sink,
	}
}

// SetPeerConnReady records whether this host child has a ready PeerConn.
// peer is the host gonka address (Phase 2 may use addr@version).
func (a *PeerRPCAdoption) SetPeerConnReady(peer string, ready bool) {
	if a == nil || peer == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ready[peer] = ready
	if a.sink == nil {
		return
	}
	a.sink.SetHostRPC(peer, PeerRPCPathH2, ready)
	a.sink.SetHostRPC(peer, PeerRPCPathJSON, !ready)
}

// BindEscrow records that escrow started talking to peer. Increments
// once per (escrow, peer). Path is h2 if that peer's PeerConn is ready,
// otherwise json.
func (a *PeerRPCAdoption) BindEscrow(escrowID, peer string) {
	if a == nil || escrowID == "" || peer == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := escrowID + "\x00" + peer
	if _, ok := a.bound[key]; ok {
		return
	}
	a.bound[key] = struct{}{}
	path := PeerRPCPathJSON
	if a.ready[peer] {
		path = PeerRPCPathH2
	}
	if a.sink != nil {
		a.sink.IncEscrowSession(path)
		if !a.ready[peer] {
			a.sink.SetHostRPC(peer, PeerRPCPathJSON, true)
		}
	}
}

// BindEscrowHosts binds each unique non-empty peer.
func (a *PeerRPCAdoption) BindEscrowHosts(escrowID string, peers []string) {
	seen := make(map[string]struct{}, len(peers))
	for _, peer := range peers {
		if peer == "" {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		a.BindEscrow(escrowID, peer)
	}
}
