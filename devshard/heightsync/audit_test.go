package heightsync_test

import (
	"testing"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

func TestAuditRing_AppendsAndListsByPeer(t *testing.T) {
	ring := heightsync.NewAuditRing(4)

	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-a", MainnetHeight: 10, MainnetBlockHash: []byte{0x0a}})
	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-b", MainnetHeight: 20, MainnetBlockHash: []byte{0x14}})
	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-a", MainnetHeight: 11, MainnetBlockHash: []byte{0x0b}})

	a := ring.List("peer-a")
	require.Len(t, a, 2)
	require.Equal(t, int64(10), a[0].MainnetHeight)
	require.Equal(t, int64(11), a[1].MainnetHeight)

	b := ring.List("peer-b")
	require.Len(t, b, 1)
	require.Equal(t, int64(20), b[0].MainnetHeight)
}

func TestAuditRing_BoundedCapacityDropsOldest(t *testing.T) {
	ring := heightsync.NewAuditRing(2)

	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-a", MainnetHeight: 1, MainnetBlockHash: []byte{0x01}})
	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-a", MainnetHeight: 2, MainnetBlockHash: []byte{0x02}})
	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-a", MainnetHeight: 3, MainnetBlockHash: []byte{0x03}})

	got := ring.List("peer-a")
	require.Len(t, got, 2)
	require.Equal(t, int64(2), got[0].MainnetHeight)
	require.Equal(t, int64(3), got[1].MainnetHeight)
}

func TestAuditRing_DefensiveCopy(t *testing.T) {
	ring := heightsync.NewAuditRing(2)

	src := []byte{0xaa, 0xbb}
	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-a", MainnetHeight: 1, MainnetBlockHash: src})
	src[0] = 0xff

	list := ring.List("peer-a")
	require.Len(t, list, 1)
	require.Equal(t, []byte{0xaa, 0xbb}, list[0].MainnetBlockHash)

	list[0].MainnetBlockHash[1] = 0xee
	reload := ring.List("peer-a")
	require.Equal(t, []byte{0xaa, 0xbb}, reload[0].MainnetBlockHash)
}

func TestAuditRing_ListPeers(t *testing.T) {
	ring := heightsync.NewAuditRing(2)
	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-a", MainnetHeight: 1})
	ring.Append(heightsync.AnchorAttestation{PeerID: "peer-b", MainnetHeight: 1})

	peers := ring.ListPeers()
	require.Len(t, peers, 2)
	require.ElementsMatch(t, []string{"peer-a", "peer-b"}, peers)
}

func TestInboundTrust(t *testing.T) {
	hs := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       5,
		MainnetBlockHashHex: "00",
	}
	require.Equal(t, heightsync.TrustUntrustedPeer, heightsync.InboundTrust(hs, &blocks.Header{Height: 4}))
	require.Equal(t, heightsync.TrustPeerAligned, heightsync.InboundTrust(hs, &blocks.Header{Height: 5}))
	require.Equal(t, heightsync.TrustPeerAligned, heightsync.InboundTrust(hs, &blocks.Header{Height: 6}))
	require.Equal(t, heightsync.AttestationTrust(""), heightsync.InboundTrust(nil, &blocks.Header{Height: 10}))
}
