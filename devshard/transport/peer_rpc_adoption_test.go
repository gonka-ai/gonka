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

func (s *adoptionSink) DeleteHostRPC(peer, mode string) {
	modes := s.host[peer]
	if modes == nil {
		return
	}
	delete(modes, mode)
	if len(modes) == 0 {
		delete(s.host, peer)
	}
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

func TestPeerRPCAdoption_ReleaseLastJSONPeerDeletesHostRPC(t *testing.T) {
	sink := newAdoptionSink()
	a := NewPeerRPCAdoption(sink)
	const peer = "gonka1host"

	a.BindEscrow("escrow-a", peer)
	a.BindEscrow("escrow-b", peer)
	a.ReleaseEscrow("escrow-a")
	if !sink.host[peer][PeerRPCPathJSON] {
		t.Fatal("host_rpc json must stay while another escrow still binds the peer")
	}

	a.ReleaseEscrow("escrow-b")
	if _, ok := sink.host[peer]; ok {
		t.Fatalf("host_rpc series must be deleted after the last bind, got %v", sink.host[peer])
	}
	if got := sink.escrow[PeerRPCPathJSON]; got != 2 {
		t.Fatalf("escrow_sessions json = %d, want 2 (counter must not decrement)", got)
	}

	a.ReleaseEscrow("escrow-b") // idempotent
	a.BindEscrow("escrow-a", peer)
	if got := sink.escrow[PeerRPCPathJSON]; got != 3 {
		t.Fatalf("re-bind after release must increment again, got %d", got)
	}
	if !sink.host[peer][PeerRPCPathJSON] {
		t.Fatal("re-bind after release must set host_rpc json again")
	}
}

func TestPeerRPCAdoption_ReleaseKeepsReadyPeerConn(t *testing.T) {
	sink := newAdoptionSink()
	a := NewPeerRPCAdoption(sink)
	const peer = "gonka1host"

	a.SetPeerConnReady(peer, true)
	a.BindEscrow("escrow-a", peer)
	a.ReleaseEscrow("escrow-a")

	if !sink.host[peer][PeerRPCPathH2] {
		t.Fatal("ready PeerConn must keep host_rpc h2 after the last escrow leaves")
	}
	if _, ok := sink.host[peer][PeerRPCPathJSON]; ok {
		t.Fatal("idle json series should be deleted when only h2 remains")
	}

	a.SetPeerConnReady(peer, false)
	if _, ok := sink.host[peer]; ok {
		t.Fatalf("dropping an idle PeerConn must delete host_rpc, got %v", sink.host[peer])
	}
}

func TestPeerRPCAdoption_TwoVersionsDoNotCollapse(t *testing.T) {
	sink := newAdoptionSink()
	a := NewPeerRPCAdoption(sink)
	const host = "gonka1host"
	v5 := host + "@v5"
	v6 := host + "@v6"

	a.SetPeerConnReady(v5, true)
	a.SetPeerConnReady(v6, true)
	a.SetPeerConnReady(v5, false)

	if !sink.host[v6][PeerRPCPathH2] {
		t.Fatal("v6 h2 must survive v5 going down")
	}
	if _, ok := sink.host[v5]; ok {
		t.Fatalf("idle v5 must be deleted, got %v", sink.host[v5])
	}
}

func TestPeerRPCAdoption_BindEscrowAddressSeesVersionedReady(t *testing.T) {
	sink := newAdoptionSink()
	a := NewPeerRPCAdoption(sink)
	const host = "gonka1host"

	a.SetPeerConnReady(host+"@v5", true)
	a.BindEscrow("escrow-a", host)

	if got := sink.escrow[PeerRPCPathH2]; got != 1 {
		t.Fatalf("escrow_sessions h2 = %d, want 1 (address bind, versioned ready)", got)
	}
	if sink.host[host][PeerRPCPathJSON] {
		t.Fatal("json host_rpc must not be set when a child PeerConn is ready")
	}
}
