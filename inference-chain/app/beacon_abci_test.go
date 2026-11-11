package app

import (
	"errors"
	"testing"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/mempool"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"
)

func beaconTestCtx() sdk.Context {
	return sdk.NewContext(nil, cmtproto.Header{}, false, log.NewNopLogger())
}

func TestBeaconExtendVote_DefaultIsEmpty(t *testing.T) {
	h := newBeaconExtendVoteHandler(log.NewNopLogger(), nil)
	resp, err := h(beaconTestCtx(), &abci.RequestExtendVote{Height: 10})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Empty(t, resp.VoteExtension)
}

func TestBeaconExtendVote_NeverErrors(t *testing.T) {
	cases := map[string]extensionBuilder{
		"panic": func(sdk.Context, *abci.RequestExtendVote) ([]byte, error) { panic("boom") },
		"error": func(sdk.Context, *abci.RequestExtendVote) ([]byte, error) {
			return []byte{1, 2, 3}, errors.New("share provider unavailable")
		},
		"nil ext": func(sdk.Context, *abci.RequestExtendVote) ([]byte, error) { return nil, nil },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			h := newBeaconExtendVoteHandler(log.NewNopLogger(), build)
			resp, err := h(beaconTestCtx(), &abci.RequestExtendVote{Height: 10})
			require.NoError(t, err, "CometBFT panics on ExtendVote errors")
			require.NotNil(t, resp)
			require.NotNil(t, resp.VoteExtension)
			require.Empty(t, resp.VoteExtension)
		})
	}
}

func TestBeaconExtendVote_PassesBuilderOutput(t *testing.T) {
	want := []byte{0xbe, 0xac, 0x04}
	h := newBeaconExtendVoteHandler(log.NewNopLogger(), func(sdk.Context, *abci.RequestExtendVote) ([]byte, error) {
		return want, nil
	})
	resp, err := h(beaconTestCtx(), &abci.RequestExtendVote{Height: 10})
	require.NoError(t, err)
	require.Equal(t, want, resp.VoteExtension)
}

func TestBeaconVerifyVoteExtension(t *testing.T) {
	const capBytes = 100
	h := newBeaconVerifyVoteExtensionHandler(log.NewNopLogger(), func(sdk.Context, int64) int { return capBytes })

	accept := map[string][]byte{
		"nil":                nil,
		"empty":              {},
		"unknown payload":    {0x01},
		"exactly at the cap": make([]byte, capBytes),
	}
	for name, ext := range accept {
		t.Run("accept "+name, func(t *testing.T) {
			resp, err := h(beaconTestCtx(), &abci.RequestVerifyVoteExtension{Height: 5, VoteExtension: ext})
			require.NoError(t, err)
			require.Equal(t, abci.ResponseVerifyVoteExtension_ACCEPT, resp.Status,
				"unknown payloads must be accepted so binary-version skew cannot reject precommits")
		})
	}

	t.Run("reject oversized", func(t *testing.T) {
		resp, err := h(beaconTestCtx(), &abci.RequestVerifyVoteExtension{
			Height:           5,
			ValidatorAddress: []byte{0xaa, 0xbb},
			VoteExtension:    make([]byte, capBytes+1),
		})
		require.NoError(t, err, "CometBFT panics on VerifyVoteExtension errors")
		require.Equal(t, abci.ResponseVerifyVoteExtension_REJECT, resp.Status)
	})

	t.Run("panicking size cap rejects without error", func(t *testing.T) {
		hp := newBeaconVerifyVoteExtensionHandler(log.NewNopLogger(), func(sdk.Context, int64) int { panic("params unavailable") })
		resp, err := hp(beaconTestCtx(), &abci.RequestVerifyVoteExtension{Height: 5, VoteExtension: []byte{1}})
		require.NoError(t, err)
		require.Equal(t, abci.ResponseVerifyVoteExtension_REJECT, resp.Status)
	})
}

func TestBeaconExtensionMaxBytes(t *testing.T) {
	// Room for a validator owning every slot: 48-byte partial + index + framing per slot.
	require.Equal(t, 1000*64+512, beaconExtensionMaxBytes(1000))
	require.Greater(t, beaconExtensionMaxBytes(1000), 1000*(48+5)+64)
	// Floor and zero-slot fallback.
	require.Equal(t, 4096, beaconExtensionMaxBytes(1))
	require.Equal(t, beaconExtensionMaxBytes(100), beaconExtensionMaxBytes(0), "0 falls back to the x/bls default slot count")
}

func TestBeaconProcessProposal_AcceptsAll(t *testing.T) {
	h := newBeaconProcessProposalHandler(log.NewNopLogger())
	resp, err := h(beaconTestCtx(), &abci.RequestProcessProposal{Height: 3, Txs: [][]byte{{0xff}, {0x00, 0x01}}})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, resp.Status)
}

func TestBeaconPrepareProposal_DelegatesToInner(t *testing.T) {
	var called bool
	inner := func(_ sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		called = true
		return &abci.ResponsePrepareProposal{Txs: req.Txs[:1]}, nil
	}
	h := newBeaconPrepareProposalHandler(log.NewNopLogger(), inner)
	resp, err := h(beaconTestCtx(), &abci.RequestPrepareProposal{
		Height: 4,
		Txs:    [][]byte{{1}, {2}},
		LocalLastCommit: abci.ExtendedCommitInfo{Votes: []abci.ExtendedVoteInfo{
			{VoteExtension: []byte{}}, {VoteExtension: []byte{9}},
		}},
	})
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, [][]byte{{1}}, resp.Txs)
}

// The pass-through used in production is the SDK default handler with a NoOp
// mempool: it keeps req.Txs order and enforces MaxTxBytes, independent of the
// node's configured app mempool.
func TestBeaconPrepareProposal_PassThroughRespectsMaxTxBytes(t *testing.T) {
	passThrough := baseapp.NewDefaultProposalHandler(mempool.NoOpMempool{}, fakeTxVerifier{}).PrepareProposalHandler()
	h := newBeaconPrepareProposalHandler(log.NewNopLogger(), passThrough)

	tx := make([]byte, 100)
	txs := [][]byte{tx, tx, tx}
	// Proto size of one 100-byte tx is 102 (tag + varint length + payload).
	resp, err := h(beaconTestCtx(), &abci.RequestPrepareProposal{Height: 4, Txs: txs, MaxTxBytes: 2*102 + 50})
	require.NoError(t, err)
	require.Len(t, resp.Txs, 2)

	resp, err = h(beaconTestCtx(), &abci.RequestPrepareProposal{Height: 4, Txs: txs, MaxTxBytes: 10_000})
	require.NoError(t, err)
	require.Equal(t, txs, resp.Txs)
}

type countingPreBlock struct{ calls int }

func (c *countingPreBlock) PreBlock(sdk.Context) (*sdk.ResponsePreBlock, error) {
	c.calls++
	return &sdk.ResponsePreBlock{ConsensusParamsChanged: true}, nil
}

func TestBeaconPreBlocker_RunsModulePreBlockers(t *testing.T) {
	mm := &countingPreBlock{}
	resp, err := newBeaconPreBlocker(mm)(beaconTestCtx(), &abci.RequestFinalizeBlock{Height: 7})
	require.NoError(t, err)
	require.Equal(t, 1, mm.calls)
	require.True(t, resp.ConsensusParamsChanged, "module PreBlock response must be propagated (x/upgrade sets it)")
}

func TestCountVoteExtensions(t *testing.T) {
	with, total := countVoteExtensions(abci.ExtendedCommitInfo{Votes: []abci.ExtendedVoteInfo{
		{}, {VoteExtension: []byte{1}}, {VoteExtension: []byte{}},
	}})
	require.Equal(t, 1, with)
	require.Equal(t, 3, total)
}

// fakeTxVerifier decodes any bytes into an opaque tx with no gas limit.
type fakeTxVerifier struct{}

type fakeTx struct{}

func (fakeTx) GetMsgs() []sdk.Msg                    { return nil }
func (fakeTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func (fakeTxVerifier) PrepareProposalVerifyTx(sdk.Tx) ([]byte, error) { return nil, nil }
func (fakeTxVerifier) ProcessProposalVerifyTx([]byte) (sdk.Tx, error) { return fakeTx{}, nil }
func (fakeTxVerifier) TxDecode([]byte) (sdk.Tx, error)                { return fakeTx{}, nil }
func (fakeTxVerifier) TxEncode(sdk.Tx) ([]byte, error)                { return nil, nil }
