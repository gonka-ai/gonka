package app_test

import (
	"bytes"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/protoio"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdked25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/app/beacon/sharefile"
	"github.com/productscience/inference/app/upgrades/v0_2_18"
	"github.com/productscience/inference/x/bls/blsutil"
	blstypes "github.com/productscience/inference/x/bls/types"
)

// beaconNet is a test app past the Step 3/4 activation with a completed key
// epoch whose slots [3,7] (threshold 3) belong to the genesis validator.
type beaconNet struct {
	t       *testing.T
	app     *app.App
	cons    []byte
	power   int64
	key     *sdked25519.PrivKey
	slotPKs [][]byte
	groupPK []byte
	height  int64 // last finalized height
}

func newBeaconNet(t *testing.T) *beaconNet {
	t.Helper()
	return newBeaconNetWithOptions(t, nil)
}

func newBeaconNetWithOptions(t *testing.T, opts map[string]interface{}) *beaconNet {
	t.Helper()
	testApp := createTestAppWithOptions(t, opts)
	t.Cleanup(func() { require.NoError(t, testApp.Close()) })
	k, ok := testGenesisValKeys.Load(testApp)
	require.True(t, ok)
	n := &beaconNet{t: t, app: testApp, key: k.(*sdked25519.PrivKey), height: 1}

	const epochID, totalSlots, degree = 3, 8, 2
	coeffs := make([]fr.Element, degree+1)
	for i := range coeffs {
		_, err := coeffs[i].SetRandom()
		require.NoError(t, err)
	}
	secrets := make([]fr.Element, totalSlots)
	for s := range secrets {
		var x fr.Element
		x.SetUint64(uint64(s) + 1)
		for i := len(coeffs) - 1; i >= 0; i-- {
			secrets[s].Mul(&secrets[s], &x)
			secrets[s].Add(&secrets[s], &coeffs[i])
		}
		n.slotPKs = append(n.slotPKs, g2Compressed(secrets[s]))
	}
	n.groupPK = g2Compressed(coeffs[0])

	seed := testApp.NewUncachedContext(false, cmtproto.Header{Height: 2, ChainID: testApp.ChainID()})
	vals, err := testApp.StakingKeeper.GetAllValidators(seed)
	require.NoError(t, err)
	oper, err := sdk.ValAddressFromBech32(vals[0].OperatorAddress)
	require.NoError(t, err)
	owner := sdk.AccAddress(oper).String()
	n.cons, err = vals[0].GetConsAddr()
	require.NoError(t, err)
	n.power = vals[0].GetConsensusPower(sdk.DefaultPowerReduction)
	testApp.SetBeaconSelfForTest(n.cons)

	require.NoError(t, testApp.BlsKeeper.SetEpochBLSDataBaseOnly(seed, blstypes.EpochBLSData{
		EpochId: epochID, ITotalSlots: totalSlots, TSlotsDegree: degree,
		DkgPhase: blstypes.DKGPhase_DKG_PHASE_COMPLETED, GroupPublicKey: n.groupPK, SlotPublicKeys: n.slotPKs,
		ValidDealers: []bool{true},
		Participants: []blstypes.BLSParticipantInfo{
			{Address: sdk.AccAddress(make([]byte, 20)).String(), SlotStartIndex: 0, SlotEndIndex: 2},
			{Address: owner, SlotStartIndex: 3, SlotEndIndex: 7},
		},
	}))
	testApp.BlsKeeper.SetCurrentSigningEpochID(seed, epochID)
	seed.KVStore(testApp.GetKey(blstypes.StoreKey)).Delete(blstypes.BeaconActivationKey)
	_, _, err = v0_2_18.EnableVoteExtensions(seed, testApp.ConsensusParamsKeeper.ParamsStore, 2) // E = 4
	require.NoError(t, err)

	shares := make([][sharefile.ScalarLen]byte, 0, 5)
	for s := 3; s <= 7; s++ {
		shares = append(shares, secrets[s].Bytes())
	}
	for h := int64(2); h <= 5; h++ {
		n.finalize(nil)
	}
	// Step 3 + 4 activation at U = 6 (the v0.2.18 handler).
	activated, err := v0_2_18.ActivateBlockBeacon(testApp.NewUncachedContext(false, cmtproto.Header{Height: 6, ChainID: testApp.ChainID()}), testApp.BlsKeeper)
	require.NoError(t, err)
	require.True(t, activated)
	n.finalize(nil) // 6: EndBlock writes PendingBeacon(7); block 7 needs no beacon tx (round 6 never pending)
	// Share file after all direct state writes (see the Step 3 test).
	provider := testApp.BeaconShares.(*beacon.FileShareProvider)
	file, err := sharefile.New(testApp.ChainID(), epochID, owner, 3, 7, n.groupPK, shares, time.Now())
	require.NoError(t, err)
	_, _, err = sharefile.WriteAtomic(provider.Dir(), file)
	require.NoError(t, err)
	provider.Reload()
	require.Eventually(t, func() bool {
		for _, st := range provider.Statuses() {
			if st.State == "valid" {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "shares verified in the background")
	return n
}

func (n *beaconNet) committed() sdk.Context {
	return n.app.NewUncachedContext(false, cmtproto.Header{Height: n.height + 1, ChainID: n.app.ChainID()})
}

func (n *beaconNet) finalize(txs [][]byte) *abci.ResponseFinalizeBlock {
	n.t.Helper()
	n.height++
	resp, err := n.app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: n.height, Hash: blockHash(n.height), Time: time.Unix(1_700_000_000+n.height, 0), Txs: txs})
	require.NoError(n.t, err)
	_, err = n.app.Commit()
	require.NoError(n.t, err)
	return resp
}

// commit returns the extended commit of round h with the given extension,
// signed by the genesis validator, and the matching LastCommit.
func (n *beaconNet) commit(h int64, ext []byte, forge bool) (abci.ExtendedCommitInfo, abci.CommitInfo) {
	n.t.Helper()
	var buf bytes.Buffer
	_, err := protoio.NewDelimitedWriter(&buf).WriteMsg(&cmtproto.CanonicalVoteExtension{
		Extension: ext, Height: h, Round: 0, ChainId: n.app.ChainID(),
	})
	require.NoError(n.t, err)
	priv := ed25519.PrivKey(n.key.Key)
	sig, err := priv.Sign(buf.Bytes())
	require.NoError(n.t, err)
	if forge {
		sig[0] ^= 1
	}
	val := abci.Validator{Address: n.cons, Power: n.power}
	return abci.ExtendedCommitInfo{Round: 0, Votes: []abci.ExtendedVoteInfo{{Validator: val, VoteExtension: ext, ExtensionSignature: sig, BlockIdFlag: cmtproto.BlockIDFlagCommit}}},
		abci.CommitInfo{Round: 0, Votes: []abci.VoteInfo{{Validator: val, BlockIdFlag: cmtproto.BlockIDFlagCommit}}}
}

func (n *beaconNet) prepare(h int64, ec abci.ExtendedCommitInfo, txs [][]byte) [][]byte {
	n.t.Helper()
	resp, err := n.app.PrepareProposal(&abci.RequestPrepareProposal{Height: h, Time: time.Unix(1_700_000_000+h, 0),
		MaxTxBytes: 1 << 20, Txs: txs, LocalLastCommit: ec, ProposerAddress: n.cons})
	require.NoError(n.t, err)
	return resp.Txs
}

func (n *beaconNet) process(h int64, txs [][]byte, lc abci.CommitInfo) abci.ResponseProcessProposal_ProposalStatus {
	n.t.Helper()
	resp, err := n.app.ProcessProposal(&abci.RequestProcessProposal{Height: h, Hash: blockHash(h), Time: time.Unix(1_700_000_000+h, 0),
		Txs: txs, ProposedLastCommit: lc, ProposerAddress: n.cons})
	require.NoError(n.t, err)
	return resp.Status
}

func missTxFor(t *testing.T, round, epoch uint64, ec abci.ExtendedCommitInfo) []byte {
	t.Helper()
	b, err := ec.Marshal()
	require.NoError(t, err)
	tx, err := blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Miss{Miss: &blstypes.BeaconMissProof{Round: round, KeyEpoch: epoch, ExtendedCommit: b}}})
	require.NoError(t, err)
	return tx
}

// TestBlockBeacon_SigmaInEveryBlock runs the real app through the Step 4
// cycle: ExtendVote(h) → PrepareProposal(h+1) puts σ_h first → ProcessProposal
// accepts it (one pairing) → FinalizeBlock(h+1) succeeds with tx 0 undecodable;
// blocks without σ, with a bad σ, or with a false proof of miss are rejected.
func TestBlockBeacon_SigmaInEveryBlock(t *testing.T) {
	n := newBeaconNet(t)
	const ACCEPT, REJECT = abci.ResponseProcessProposal_ACCEPT, abci.ResponseProcessProposal_REJECT

	for i := 0; i < 3; i++ {
		h := n.height + 1 // the height being voted on
		ext, err := n.app.ExtendVote(n.committed(), &abci.RequestExtendVote{Height: h, Hash: blockHash(h)})
		require.NoError(t, err)
		n.app.WaitBeaconCollectorIdleForTest(t)
		require.NotEmpty(t, ext.VoteExtension)
		ec, lc := n.commit(h, ext.VoteExtension, false)
		n.finalize(nil) // block h

		// An undecodable mempool tx is dropped by the SDK pass-through; the
		// beacon tx is never subject to it (R4).
		txs := n.prepare(h+1, ec, [][]byte{[]byte("not-an-sdk-tx")})
		require.Len(t, txs, 1)
		ib, err := blstypes.DecodeBeaconTx(txs[0])
		require.NoError(t, err)
		s := ib.GetSigma()
		require.NotNil(t, s, "σ, not a proof of miss")
		require.Equal(t, uint64(h), s.Round)
		pending, ok := n.app.BlsKeeper.GetPendingBeacon(n.committed(), uint64(h))
		require.True(t, ok)
		require.True(t, blsutil.VerifySignature(pending.Msg, s.Sigma, n.groupPK), "σ_h verifies under the group key")

		require.Equal(t, ACCEPT, n.process(h+1, txs, lc))
		require.Equal(t, REJECT, n.process(h+1, txs[1:], lc), "no beacon tx: the proposer is replaced")
		bad := append([]byte(nil), s.Sigma...)
		bad[len(bad)-1] ^= 1
		badTx, err := blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Sigma{Sigma: &blstypes.BeaconSigma{Round: s.Round, KeyEpoch: s.KeyEpoch, Sigma: bad}}})
		require.NoError(t, err)
		require.Equal(t, REJECT, n.process(h+1, [][]byte{badTx}, lc), "bad σ")
		require.Equal(t, REJECT, n.process(h+1, [][]byte{missTxFor(t, uint64(h), s.KeyEpoch, ec)}, lc),
			"proof of miss while the commit has enough slots")

		resp := n.finalize(append(txs, []byte("other-tx"))) // block h+1 carries σ_h
		require.Len(t, resp.TxResults, 2)
		require.NotZero(t, resp.TxResults[0].Code, "the beacon tx is not an SDK tx")

		// Step 5: FinalizeBlock(h+1) recorded round h, verifiable from the record alone.
		rec, ok := n.app.BlsKeeper.GetBlockBeacon(n.committed(), uint64(h))
		require.True(t, ok)
		require.Equal(t, blstypes.BeaconStatus_BEACON_STATUS_OK, rec.Status)
		require.Equal(t, s.Sigma, rec.Sigma)
		require.True(t, blsutil.VerifySignature(rec.Msg, rec.Sigma, n.groupPK))
		_, ok = n.app.BlsKeeper.GetPendingBeacon(n.committed(), uint64(h))
		require.False(t, ok, "pending message consumed")
		latest, ok := n.app.BlsKeeper.GetLatestBlockBeacon(n.committed())
		require.True(t, ok)
		require.Equal(t, uint64(h), latest.Round)
	}
}

// TestBlockBeacon_ProofOfMiss: a round whose only precommit has an empty
// extension yields a proof of miss that passes the real extension-signature
// check; a forged signature is rejected; the chain keeps going.
func TestBlockBeacon_ProofOfMiss(t *testing.T) {
	n := newBeaconNet(t)
	h := n.height + 1
	ec, lc := n.commit(h, []byte{}, false) // no partials in the commit
	n.finalize(nil)

	txs := n.prepare(h+1, ec, nil)
	require.Len(t, txs, 1)
	ib, err := blstypes.DecodeBeaconTx(txs[0])
	require.NoError(t, err)
	require.NotNil(t, ib.GetMiss(), "below threshold: proof of miss")
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, n.process(h+1, txs, lc))

	forged, flc := n.commit(h, []byte{}, true)
	require.Equal(t, abci.ResponseProcessProposal_REJECT, n.process(h+1, [][]byte{missTxFor(t, uint64(h), ib.GetMiss().KeyEpoch, forged)}, flc),
		"extension signature does not verify")

	n.finalize(txs)
	require.Equal(t, int64(h+1), n.height)

	rec, ok := n.app.BlsKeeper.GetBlockBeacon(n.committed(), uint64(h))
	require.True(t, ok)
	require.Equal(t, blstypes.BeaconStatus_BEACON_STATUS_MISS_THRESHOLD, rec.Status)
	require.Zero(t, rec.ValidSlots)
	require.Empty(t, rec.Sigma)
}

// R7: the beacon handlers ignore the local app-side mempool setting: an app
// with mempool.max-txs = 5000 accepts the same proposal (undecodable Txs[1:]
// included) that the default would.
func TestBlockBeacon_ProcessProposalIgnoresMempoolConfig(t *testing.T) {
	for _, maxTxs := range []int{-1, 5000} {
		testApp := createTestAppWithOptions(t, map[string]interface{}{"mempool.max-txs": maxTxs})
		resp, err := testApp.ProcessProposal(&abci.RequestProcessProposal{Height: 2, Hash: blockHash(2), Time: time.Unix(1_700_000_002, 0),
			Txs: [][]byte{[]byte("garbage-1"), []byte("garbage-2")}})
		require.NoError(t, err)
		require.Equal(t, abci.ResponseProcessProposal_ACCEPT, resp.Status, "max-txs=%d", maxTxs)
		require.NoError(t, testApp.Close())
	}
}

// The switch is read from the environment (INFERENCED_BLS_BEACON_TEST_WITHHOLD_SIGMA
// maps to this key) and makes the real app propose without σ.
func TestBlockBeacon_WithholdSwitchInApp(t *testing.T) {
	n := newBeaconNetWithOptions(t, map[string]interface{}{app.FlagBeaconTestWithholdSigma: true})
	h := n.height + 1
	ext, err := n.app.ExtendVote(n.committed(), &abci.RequestExtendVote{Height: h, Hash: blockHash(h)})
	require.NoError(t, err)
	n.app.WaitBeaconCollectorIdleForTest(t)
	ec, lc := n.commit(h, ext.VoteExtension, false)
	n.finalize(nil)
	txs := n.prepare(h+1, ec, nil)
	require.Empty(t, txs, "no beacon tx")
	require.Equal(t, abci.ResponseProcessProposal_REJECT, n.process(h+1, txs, lc), "rejected: the next proposer takes over")
}

// Step 5: a block without a beacon tx that reaches FinalizeBlock anyway (block
// sync or replay of a block this binary never checked) is recorded as
// MISS_NO_INJECTION; FinalizeBlock never fails because of the beacon.
func TestBlockBeacon_FinalizeWithoutBeaconTx(t *testing.T) {
	n := newBeaconNet(t)
	h := n.height + 1
	n.finalize(nil)                                  // block h: EndBlock(h) writes PendingBeacon(h+1); PendingBeacon(h) exists since EndBlock(h-1)
	resp := n.finalize([][]byte{[]byte("plain tx")}) // block h+1 without σ_h
	require.NotNil(t, resp)
	rec, ok := n.app.BlsKeeper.GetBlockBeacon(n.committed(), uint64(h))
	require.True(t, ok)
	require.Equal(t, blstypes.BeaconStatus_BEACON_STATUS_MISS_NO_INJECTION, rec.Status)
	require.NotEmpty(t, rec.Msg)

	// Every round from here on keeps getting a record (one per block).
	for i := 0; i < 3; i++ {
		n.finalize(nil)
		_, ok := n.app.BlsKeeper.GetBlockBeacon(n.committed(), uint64(n.height-1))
		require.True(t, ok, "round %d recorded", n.height-1)
	}
}
