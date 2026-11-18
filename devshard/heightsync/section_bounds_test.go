package heightsync_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
)

func honestSection(dir string) *heightsync.HeightSyncSection {
	return &heightsync.HeightSyncSection{
		ChainID:               "gonka-mainnet",
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         1_000_000,
		MainnetBlockHashHex:   strings.Repeat("ab", heightsync.MaxObservedBlockHashBytes),
		TimestampUnixMs:       1_700_000_000_000,
		Direction:             dir,
		OriginatorSenderID:    "gonka1qypqxpq9qcrsszg2pvxq6rs0zqg3yyc5lzv7xu",
		OriginatorTimestampMs: 1_700_000_000_000,
		SenderSignature:       make([]byte, heightsync.MaxOriginSignatureBytes),
	}
}

func TestCheckSectionWireBounds(t *testing.T) {
	require.NoError(t, heightsync.CheckSectionWireBounds(nil))
	require.NoError(t, heightsync.CheckSectionWireBounds(honestSection("response")))

	prefixed := honestSection("response")
	prefixed.MainnetBlockHashHex = "0x" + prefixed.MainnetBlockHashHex
	require.NoError(t, heightsync.CheckSectionWireBounds(prefixed))

	cases := map[string]func(*heightsync.HeightSyncSection){
		"mainnet_block_hash_hex": func(s *heightsync.HeightSyncSection) { s.MainnetBlockHashHex = strings.Repeat("a", 1<<20) },
		"originator_sender_id":   func(s *heightsync.HeightSyncSection) { s.OriginatorSenderID = strings.Repeat("g", 1<<20) },
		"chain_id":               func(s *heightsync.HeightSyncSection) { s.ChainID = strings.Repeat("c", 51) },
		"proof_type":             func(s *heightsync.HeightSyncSection) { s.ProofType = strings.Repeat("p", 33) },
		"direction":              func(s *heightsync.HeightSyncSection) { s.Direction = strings.Repeat("d", 33) },
		"sender_signature":       func(s *heightsync.HeightSyncSection) { s.SenderSignature = make([]byte, 66) },
	}
	for field, mutate := range cases {
		t.Run(field, func(t *testing.T) {
			sec := honestSection("response")
			mutate(sec)
			err := heightsync.CheckSectionWireBounds(sec)
			require.ErrorIs(t, err, heightsync.ErrSectionOversized)
			require.Contains(t, err.Error(), field)
		})
	}
}

func badAcks(n int) []*types.DevshardTx {
	txs := make([]*types.DevshardTx, 0, n)
	for i := 0; i < n; i++ {
		txs = append(txs, ackTxAt(1, uint32(i%8), 1, []byte{0x01}))
	}
	return txs
}

func TestCheckEnvelopeBinding_OversizedSectionMarksNothing(t *testing.T) {
	sec := honestSection("response")
	sec.MainnetBlockHashHex = strings.Repeat("a", 1<<20)
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   badAcks(100),
		Sec:   sec,
	}, heightsync.DefaultHeartbeatConfig())
	require.Empty(t, marks)
}

func TestCheckEnvelopeBinding_CapsResponseLegMarks(t *testing.T) {
	sec := honestSection("response")
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   badAcks(5000),
		Sec:   sec,
	}, heightsync.DefaultHeartbeatConfig())
	require.Len(t, marks, heightsync.MaxEnvelopeBindingMarks)

	want, err := heightsync.CanonicalOriginBytes(sec)
	require.NoError(t, err)
	for _, m := range marks {
		require.Equal(t, heightsync.MarkDisputeOriginator, m.Kind)
		require.Equal(t, want, m.Blob)
		require.LessOrEqual(t, len(m.Blob), heightsync.MaxMarkBlobBytes)
		require.Equal(t, sec.SenderSignature, m.Sig)
	}
}

func TestCheckEnvelopeBinding_CapsRequestLegMarks(t *testing.T) {
	sec := honestSection("request")
	sec.OriginatorSenderID = ""
	sec.OriginatorTimestampMs = 0
	hash := []byte{0xaa}
	txs := make([]*types.DevshardTx, 0, 5000)
	for i := 0; i < 5000; i++ {
		txs = append(txs, hbTx(1, 3, hash, nil))
	}
	body := make([]byte, 1<<20)
	leg := &heightsync.RequestLegEvidence{Body: body, Sig: []byte{1, 2, 3}, Timestamp: 1_700_000_000, EscrowID: "escrow-1"}
	in := heightsync.LogPlaneInput{Nonce: 2, Txs: txs, Sec: sec, RequestLeg: leg}

	marks := heightsync.CheckEnvelopeBinding(in, heightsync.DefaultHeartbeatConfig())
	require.Len(t, marks, heightsync.MaxEnvelopeBindingMarks)
	digest := heightsync.CanonicalRequestLegBytes(leg.EscrowID, leg.Body, leg.Timestamp)
	for _, m := range marks {
		require.Equal(t, heightsync.MarkDisputeCarrier, m.Kind)
		require.Equal(t, digest, m.Blob)
		require.Equal(t, leg.EscrowID, m.EscrowID)
		require.Equal(t, leg.Timestamp, m.Timestamp)
	}
}

func TestCheckEnvelopeBinding_CapsAdmissionMarks(t *testing.T) {
	sec := honestSection("response")
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce:        2,
		Txs:          badAcks(5000),
		Sec:          sec,
		LocalAligned: 1_000_000,
	}, heightsync.DefaultHeartbeatConfig())
	require.Len(t, marks, heightsync.MaxEnvelopeBindingMarks)
}

func TestCheckEnvelopeBinding_CapsAdmissionOnlyMarks(t *testing.T) {
	sec := honestSection("response")
	txs := make([]*types.DevshardTx, 0, 5000)
	for i := 0; i < 5000; i++ {
		txs = append(txs, ackTxAt(1, uint32(i%8), uint64(sec.MainnetHeight), []byte{0x01}))
	}
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce:        2,
		Txs:          txs,
		Sec:          sec,
		LocalAligned: 1,
	}, heightsync.DefaultHeartbeatConfig())
	require.Len(t, marks, heightsync.MaxEnvelopeBindingMarks)
	for _, m := range marks {
		require.Equal(t, heightsync.MarkAdmissionDelta, m.Kind)
	}
}
