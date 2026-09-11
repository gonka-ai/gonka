package transport

import "testing"

type adoptionSink struct {
	escrow map[string]int
	host   map[string]map[string]bool
}

func newAdoptionSink() *adoptionSink {
	return &adoptionSink{
		escrow: make(map[string]int),
		host:   make(map[string]map[string]bool),
	}
}

func (s *adoptionSink) IncEscrowSession(path string) {
	s.escrow[path]++
}

func (s *adoptionSink) SetHostRPC(peer, mode string, on bool) {
	if s.host[peer] == nil {
		s.host[peer] = make(map[string]bool)
	}
	s.host[peer][mode] = on
}

func TestPeerRPCAdoption_TwoEscrowsOnePeerConn(t *testing.T) {
	sink := newAdoptionSink()
	a := NewPeerRPCAdoption(sink)
	const peer = "gonka1host"

	a.SetPeerConnReady(peer, true)
	a.BindEscrow("escrow-a", peer)
	a.BindEscrow("escrow-b", peer)
	a.BindEscrow("escrow-a", peer) // already bound

	if got := sink.escrow[PeerRPCPathH2]; got != 2 {
		t.Fatalf("escrow_sessions h2 = %d, want 2", got)
	}
	if got := sink.escrow[PeerRPCPathJSON]; got != 0 {
		t.Fatalf("escrow_sessions json = %d, want 0", got)
	}
	if !sink.host[peer][PeerRPCPathH2] {
		t.Fatal("host_rpc h2 should be on once for the shared PeerConn")
	}
	if sink.host[peer][PeerRPCPathJSON] {
		t.Fatal("host_rpc json should be off when PeerConn is ready")
	}
}

func TestPeerRPCAdoption_JSONWhenNoPeerConn(t *testing.T) {
	sink := newAdoptionSink()
	a := NewPeerRPCAdoption(sink)
	a.BindEscrowHosts("escrow-1", []string{"gonka1a", "gonka1a", "gonka1b"})

	if got := sink.escrow[PeerRPCPathJSON]; got != 2 {
		t.Fatalf("escrow_sessions json = %d, want 2 unique hosts", got)
	}
	if !sink.host["gonka1a"][PeerRPCPathJSON] || !sink.host["gonka1b"][PeerRPCPathJSON] {
		t.Fatal("each host should show json when there is no PeerConn")
	}
}
