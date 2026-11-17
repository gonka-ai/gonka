package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"devshard/transport"
)

// Test flow:
//  1. Bind an escrow to one host whose peer connection never became ready, through the transport's adoption tracker.
//  2. Assert one JSON session is counted and the host's JSON gauge reads 1.
//  3. Release the escrow and assert the host's gauge series is gone.
func TestAnEscrowBoundOverJSONIsCountedAndForgottenWhenReleased(t *testing.T) {
	recorder := NewPeerRPCRecorder(New())
	adoption := transport.NewPeerRPCAdoption(recorder)
	peer := transport.PeerChildID("host-a", "/v2")

	adoption.BindEscrowHosts("escrow-1", []string{peer})

	if got := testutil.ToFloat64(recorder.sessions.WithLabelValues(transport.PeerRPCPathJSON)); got != 1 {
		t.Fatalf("escrow sessions{path=json} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(recorder.hosts.WithLabelValues(peer, transport.PeerRPCPathJSON)); got != 1 {
		t.Fatalf("host rpc{mode=json} = %v, want 1", got)
	}
	adoption.ReleaseEscrow("escrow-1")
	if got := testutil.CollectAndCount(recorder.hosts); got != 0 {
		t.Fatalf("host rpc series = %d after release, want 0", got)
	}
}
