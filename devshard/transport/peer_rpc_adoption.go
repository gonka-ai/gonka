package transport

import (
	"strings"
	"sync"
)

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
	DeleteHostRPC(peer, mode string)
}

// PeerRPCAdoption records which path an escrow uses to talk to a host.
// PeerConn is per (host, version); escrow_sessions_total counts escrow work
// on that slot, not Attaches. Two escrows sharing one ready PeerConn
// increment the counter twice and set the host gauge once.
type PeerRPCAdoption struct {
	mu    sync.Mutex
	ready map[string]bool
	bound map[string]map[string]struct{} // escrowID -> peers
	hosts map[string]int                 // peer -> live escrow count
	sink  PeerRPCAdoptionSink
}

func NewPeerRPCAdoption(sink PeerRPCAdoptionSink) *PeerRPCAdoption {
	return &PeerRPCAdoption{
		ready: make(map[string]bool),
		bound: make(map[string]map[string]struct{}),
		hosts: make(map[string]int),
		sink:  sink,
	}
}

// SetPeerConnReady records whether this host child has a ready PeerConn.
// peer is addr@version (same as the PeerConn registry).
func (a *PeerRPCAdoption) SetPeerConnReady(peer string, ready bool) {
	if a == nil || peer == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if ready {
		a.ready[peer] = true
		if a.sink == nil {
			return
		}
		a.sink.SetHostRPC(peer, PeerRPCPathH2, true)
		a.sink.SetHostRPC(peer, PeerRPCPathJSON, false)
		return
	}
	delete(a.ready, peer)
	if a.hosts[peer] > 0 {
		if a.sink != nil {
			a.sink.SetHostRPC(peer, PeerRPCPathH2, false)
			a.sink.SetHostRPC(peer, PeerRPCPathJSON, true)
		}
		return
	}
	a.deleteIdleHostLocked(peer)
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
	peers := a.bound[escrowID]
	if _, ok := peers[peer]; ok {
		return
	}
	if peers == nil {
		peers = make(map[string]struct{})
		a.bound[escrowID] = peers
	}
	peers[peer] = struct{}{}
	a.hosts[peer]++
	path := PeerRPCPathJSON
	if a.childReady(peer) {
		path = PeerRPCPathH2
	}
	if a.sink != nil {
		a.sink.IncEscrowSession(path)
		if !a.childReady(peer) {
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

// ReleaseEscrow drops every (escrow, peer) bind. Idempotent. The
// escrow_sessions_total counter is not decremented. host_rpc series for a
// peer with no remaining binds are deleted unless a PeerConn is still ready
// (finding 49).
func (a *PeerRPCAdoption) ReleaseEscrow(escrowID string) {
	if a == nil || escrowID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	peers := a.bound[escrowID]
	if len(peers) == 0 {
		delete(a.bound, escrowID)
		return
	}
	delete(a.bound, escrowID)
	for peer := range peers {
		a.hosts[peer]--
		if a.hosts[peer] > 0 {
			continue
		}
		delete(a.hosts, peer)
		if a.childReady(peer) {
			if a.sink != nil {
				a.sink.DeleteHostRPC(peer, PeerRPCPathJSON)
			}
			continue
		}
		a.deleteIdleHostLocked(peer)
	}
}

// childReady is true if this exact id is ready, or (when peer is a bare
// gonka address) any addr@version child of that host is ready. BindEscrowHosts
// still passes addresses; PeerConn reports ready as addr@version (finding 8).
func (a *PeerRPCAdoption) childReady(peer string) bool {
	if a.ready[peer] {
		return true
	}
	prefix := peer + "@"
	for id, ok := range a.ready {
		if ok && strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

func (a *PeerRPCAdoption) deleteIdleHostLocked(peer string) {
	delete(a.ready, peer)
	if a.sink == nil {
		return
	}
	a.sink.DeleteHostRPC(peer, PeerRPCPathH2)
	a.sink.DeleteHostRPC(peer, PeerRPCPathJSON)
}
