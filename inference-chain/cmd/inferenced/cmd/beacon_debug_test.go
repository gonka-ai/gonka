package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/productscience/inference/x/bls/blsutil"
	blst "github.com/supranational/blst/bindings/go"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	blstypes "github.com/productscience/inference/x/bls/types"
)

func TestDecodeBeaconTxView(t *testing.T) {
	sigma := bytes.Repeat([]byte{7}, 48)
	tx, err := blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Sigma{Sigma: &blstypes.BeaconSigma{
		Round: 12, KeyEpoch: 3, Sigma: sigma, ContributorsHint: []byte{0x05, 0x80},
	}}})
	require.NoError(t, err)
	v, err := decodeBeaconTxView(base64.StdEncoding.EncodeToString(tx))
	require.NoError(t, err)
	require.Equal(t, "sigma", v.Outcome)
	require.Equal(t, uint64(12), v.Round)
	require.Equal(t, 48, v.SigmaLen)
	require.Equal(t, []uint32{0, 2, 15}, v.HintSlots)

	ec := abci.ExtendedCommitInfo{Votes: []abci.ExtendedVoteInfo{{
		Validator: abci.Validator{Address: []byte{0xab, 0xcd}, Power: 9}, VoteExtension: []byte{1, 2, 3}, BlockIdFlag: cmtproto.BlockIDFlagCommit,
	}}}
	b, err := ec.Marshal()
	require.NoError(t, err)
	tx, err = blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Miss{Miss: &blstypes.BeaconMissProof{Round: 13, KeyEpoch: 3, ExtendedCommit: b}}})
	require.NoError(t, err)
	v, err = decodeBeaconTxView(hex.EncodeToString(tx))
	require.NoError(t, err)
	require.Equal(t, "miss", v.Outcome)
	require.Equal(t, []beaconVoteView{{Validator: "ABCD", Power: 9, Flag: "BLOCK_ID_FLAG_COMMIT", ExtLen: 3}}, v.Votes)

	_, err = decodeBeaconTxView(base64.StdEncoding.EncodeToString([]byte("plain tx")))
	require.Error(t, err)
	_, err = decodeBeaconTxView("%%%")
	require.Error(t, err)
}

func TestVerifyBeaconRecord(t *testing.T) {
	var sk [32]byte
	sk[0] = 7 // little-endian scalar 7
	pk := blst.P2Generator().Mult(sk[:], 255).ToAffine().Compress()
	msg := bytes.Repeat([]byte{3}, 32)
	sig, err := blsutil.SignPartials(msg, [][32]byte{sk})
	require.NoError(t, err)

	ok := blstypes.BeaconRecord{Round: 5, KeyEpoch: 1, Status: blstypes.BeaconStatus_BEACON_STATUS_OK, Sigma: sig, Msg: msg}
	require.NoError(t, verifyBeaconRecord(ok, pk))
	bad := ok
	bad.Msg = bytes.Repeat([]byte{4}, 32)
	require.Error(t, verifyBeaconRecord(bad, pk))
	miss := blstypes.BeaconRecord{Round: 6, Status: blstypes.BeaconStatus_BEACON_STATUS_MISS_THRESHOLD}
	require.ErrorContains(t, verifyBeaconRecord(miss, pk), "no σ")
}
