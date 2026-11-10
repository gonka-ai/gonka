package app_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	dcrdecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/productscience/inference/app"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// Exercise the pinned SDK's header propagation and the real ante, message, bank,
// commit and query paths, rather than injecting a hash into a keeper context.
func TestDevshardEscrow_CurrentBlockHashViaFinalizeBlock(t *testing.T) {
	for _, mode := range []string{"direct", "optimistic", "replaced-proposal"} {
		t.Run(mode, func(t *testing.T) {
			testApp, key, creator, weights, escrowParams, now := setupDevshardBlockHashApp(t, nil)
			if mode != "direct" {
				baseapp.SetOptimisticExecution()(testApp.BaseApp)
			}
			const modelID = "block-hash-test-model"

			checkCtx := testApp.NewContext(true)
			account := testApp.AccountKeeper.GetAccount(checkCtx, creator)
			amount := escrowParams.MinAmount
			tx, err := simtestutil.GenSignedMockTx(rand.New(rand.NewSource(1)), testApp.TxConfig(), []sdk.Msg{&inferencetypes.MsgCreateDevshardEscrow{Creator: creator.String(), Amount: amount, ModelId: modelID}}, sdk.NewCoins(), simtestutil.DefaultGenTxGas, TallyTestChainID, []uint64{account.GetAccountNumber()}, []uint64{account.GetSequence()}, key)
			require.NoError(t, err)
			txBytes, err := testApp.TxConfig().TxEncoder()(tx)
			require.NoError(t, err)

			_, _, err = testApp.Simulate(txBytes)
			require.NoError(t, err, "simulation must tolerate an unavailable current block hash")
			_, found := testApp.InferenceKeeper.GetDevshardEscrow(checkCtx, 1)
			require.False(t, found, "simulation must not persist an escrow")
			checkResp, err := testApp.CheckTx(&abci.RequestCheckTx{Tx: txBytes})
			require.NoError(t, err)
			require.Zero(t, checkResp.Code, "CheckTx log=%s", checkResp.Log)

			blockHash := bytes.Repeat([]byte{0x42}, 32)
			require.NotEqual(t, blockHash, testApp.LastCommitID().Hash)
			before := testApp.BankKeeper.GetBalance(checkCtx, creator, inferencetypes.BaseCoin)
			if mode != "direct" {
				proposal := &abci.RequestProcessProposal{Height: testApp.LastBlockHeight() + 1, Time: now.Add(time.Second), Hash: blockHash, Txs: [][]byte{txBytes}}
				if mode == "replaced-proposal" {
					old := *proposal
					old.Hash = bytes.Repeat([]byte{0x41}, 32)
					response, err := testApp.ProcessProposal(&old)
					require.NoError(t, err)
					require.Equal(t, abci.ResponseProcessProposal_ACCEPT, response.Status)
				}
				response, err := testApp.ProcessProposal(proposal)
				require.NoError(t, err)
				require.Equal(t, abci.ResponseProcessProposal_ACCEPT, response.Status)
			}
			finalized, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: testApp.LastBlockHeight() + 1, Time: now.Add(time.Second), Hash: blockHash, Txs: [][]byte{txBytes}})
			require.NoError(t, err)
			require.Len(t, finalized.TxResults, 1)
			require.Zero(t, finalized.TxResults[0].Code, "FinalizeBlock log=%s", finalized.TxResults[0].Log)
			var escrowID string
			for _, event := range finalized.TxResults[0].Events {
				if event.Type == "devshard_escrow_created" {
					for _, attr := range event.Attributes {
						if attr.Key == "escrow_id" {
							escrowID = attr.Value
						}
					}
				}
			}
			require.Equal(t, "1", escrowID, "gateway must still find the ID in the create transaction's events")
			_, err = testApp.Commit()
			require.NoError(t, err)

			queryCtx := testApp.NewContext(true)
			query, err := testApp.InferenceKeeper.DevshardEscrow(queryCtx, &inferencetypes.QueryGetDevshardEscrowRequest{Id: 1})
			require.NoError(t, err)
			require.True(t, query.Found, "slots must be visible without waiting for another block")
			require.NotNil(t, query.Escrow)
			require.Equal(t, hex.EncodeToString(blockHash), query.Escrow.AppHash)
			sorted, total := calculations.PrepareSortedEntries(weights)
			require.Equal(t, calculations.GetSlotsFromSorted(hex.EncodeToString(blockHash), fmt.Sprintf("devshard_escrow:%d", query.Escrow.Id), modelID, sorted, total, int(escrowParams.GroupSize)), query.Escrow.Slots)
			after := testApp.BankKeeper.GetBalance(queryCtx, creator, inferencetypes.BaseCoin)
			require.Equal(t, fmt.Sprint(amount), before.Amount.Sub(after.Amount).String(), "create must lock exactly the escrow amount")

		})
	}
}

func setupDevshardBlockHashApp(t *testing.T, weights map[string]int64) (*app.App, *secp256k1.PrivKey, sdk.AccAddress, map[string]int64, *inferencetypes.DevshardEscrowParams, time.Time) {
	t.Helper()
	testApp := createTestApp(t)
	now := time.Now().UTC()
	ctx := testApp.NewUncachedContext(false, cmtproto.Header{Height: testApp.LastBlockHeight(), ChainID: TallyTestChainID, Time: now})
	key := secp256k1.GenPrivKey()
	creator := sdk.AccAddress(key.PubKey().Address())
	fundAccount(t, ctx, testApp, creator, key.PubKey())

	const epochIndex = uint64(5)
	const modelID = "block-hash-test-model"
	require.NoError(t, testApp.InferenceKeeper.SetEffectiveEpochIndex(ctx, epochIndex))
	require.NoError(t, testApp.InferenceKeeper.SetEpoch(ctx, &inferencetypes.Epoch{Index: epochIndex, PocStartBlockHeight: 1}))
	if weights == nil {
		weights = make(map[string]int64)
		for i := 0; i < 20; i++ {
			addr := sdk.AccAddress(bytes.Repeat([]byte{byte(i + 1)}, 20)).String()
			weights[addr] = 100
		}
	}
	groupWeights := make([]*inferencetypes.ValidationWeight, 0, len(weights))
	var total int64
	for addr, weight := range weights {
		groupWeights = append(groupWeights, &inferencetypes.ValidationWeight{MemberAddress: addr, Weight: weight})
		total += weight
		require.NoError(t, testApp.InferenceKeeper.SetParticipant(ctx, inferencetypes.Participant{Index: addr, Address: addr, Status: inferencetypes.ParticipantStatus_ACTIVE, CurrentEpochStats: inferencetypes.NewCurrentEpochStats()}))
	}
	testApp.InferenceKeeper.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: epochIndex, PocStartBlockHeight: 1, ModelId: modelID, ValidationWeights: groupWeights, TotalWeight: total})
	params, err := testApp.InferenceKeeper.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams.AllowedCreatorAddresses = []string{creator.String()}
	require.NoError(t, testApp.InferenceKeeper.SetParams(ctx, params))

	_, err = testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: testApp.LastBlockHeight() + 1, Time: now, Hash: bytes.Repeat([]byte{0x10}, 32)})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)

	return testApp, key, creator, weights, params.DevshardEscrowParams, now
}

func signDevshardBlockHashTx(t *testing.T, testApp *app.App, key *secp256k1.PrivKey, msgs ...sdk.Msg) []byte {
	t.Helper()
	addr := sdk.AccAddress(key.PubKey().Address())
	account := testApp.AccountKeeper.GetAccount(testApp.NewContext(true), addr)
	tx, err := simtestutil.GenSignedMockTx(rand.New(rand.NewSource(1)), testApp.TxConfig(), msgs, sdk.NewCoins(), simtestutil.DefaultGenTxGas, TallyTestChainID, []uint64{account.GetAccountNumber()}, []uint64{account.GetSequence()}, key)
	require.NoError(t, err)
	encoded, err := testApp.TxConfig().TxEncoder()(tx)
	require.NoError(t, err)
	return encoded
}

// A creator can pre-sign a create+settle transaction without knowing the block
// hash. If settlement succeeds only for a lucky draw, atomic rollback otherwise
// lets the creator retry without committing escrow funds to the unlucky draws.
func TestDevshardEscrow_AtomicCreateAndSettleCannotSelectCommittee(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	attacker := secp256k1.GenPrivKeyFromSecret([]byte("devshard-atomic-attacker"))
	honest := secp256k1.GenPrivKeyFromSecret([]byte("devshard-atomic-honest"))
	attackerAddr := sdk.AccAddress(attacker.PubKey().Address()).String()
	honestAddr := sdk.AccAddress(honest.PubKey().Address()).String()
	weights := map[string]int64{attackerAddr: 100, honestAddr: 100}
	sorted, total := calculations.PrepareSortedEntries(weights)
	const modelID = "block-hash-test-model"
	quorum := 2*16/3 + 1
	var favorable, unfavorable []byte
	for i := 0; i < 1<<18 && (favorable == nil || unfavorable == nil); i++ {
		hash := sha256.Sum256([]byte(fmt.Sprintf("atomic-create-settle-block-%d", i)))
		slots := calculations.GetSlotsFromSorted(hex.EncodeToString(hash[:]), "devshard_escrow:1", modelID, sorted, total, 16)
		matches := true
		for _, addr := range slots[:quorum] {
			matches = matches && addr == attackerAddr
		}
		if matches && favorable == nil {
			favorable = append([]byte(nil), hash[:]...)
		}
		if !matches && unfavorable == nil {
			unfavorable = append([]byte(nil), hash[:]...)
		}
	}
	require.NotEmpty(t, favorable)
	require.NotEmpty(t, unfavorable)
	for _, tc := range []struct {
		name string
		hash []byte
	}{{"unfavorable", unfavorable}, {"favorable", favorable}} {
		t.Run(tc.name, func(t *testing.T) {
			testApp, key, creator, _, params, now := setupDevshardBlockHashApp(t, weights)
			settle := devshardBlockHashSettlement(t, creator.String(), attacker, quorum)
			tx := signDevshardBlockHashTx(t, testApp, key,
				&inferencetypes.MsgCreateDevshardEscrow{Creator: creator.String(), Amount: params.MinAmount, ModelId: modelID}, settle)
			result, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: testApp.LastBlockHeight() + 1, Time: now.Add(time.Second), Hash: tc.hash, Txs: [][]byte{tx}})
			require.NoError(t, err)
			require.Len(t, result.TxResults, 1)
			require.NotZero(t, result.TxResults[0].Code, "same-block settlement must not make creation conditional on the committee")
			require.Contains(t, result.TxResults[0].Log, "cannot settle in its creation block")
			_, err = testApp.Commit()
			require.NoError(t, err)
			_, found := testApp.InferenceKeeper.GetDevshardEscrow(testApp.NewContext(true), 1)
			require.False(t, found, "failed atomic transaction must roll back escrow creation")
		})
	}

	t.Run("next-block-settlement-with-rotation", func(t *testing.T) {
		testApp, key, creator, _, params, now := setupDevshardBlockHashApp(t, weights)
		create := signDevshardBlockHashTx(t, testApp, key, &inferencetypes.MsgCreateDevshardEscrow{Creator: creator.String(), Amount: params.MinAmount, ModelId: modelID})
		result, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: testApp.LastBlockHeight() + 1, Time: now.Add(time.Second), Hash: favorable, Txs: [][]byte{create}})
		require.NoError(t, err)
		require.Zero(t, result.TxResults[0].Code, "%s", result.TxResults[0].Log)
		_, err = testApp.Commit()
		require.NoError(t, err)
		settle := signDevshardBlockHashTx(t, testApp, key,
			&inferencetypes.MsgCreateDevshardEscrow{Creator: creator.String(), Amount: params.MinAmount, ModelId: modelID},
			devshardBlockHashSettlement(t, creator.String(), attacker, quorum))
		_, _, err = testApp.Simulate(settle)
		require.NoError(t, err, "simulation immediately after create must not mistake the last committed header for the settlement block")
		nextHash := sha256.Sum256(favorable)
		result, err = testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: testApp.LastBlockHeight() + 1, Time: now.Add(2 * time.Second), Hash: nextHash[:], Txs: [][]byte{settle}})
		require.NoError(t, err)
		require.Zero(t, result.TxResults[0].Code, "%s", result.TxResults[0].Log)
		_, err = testApp.Commit()
		require.NoError(t, err)
		escrow, found := testApp.InferenceKeeper.GetDevshardEscrow(testApp.NewContext(true), 1)
		require.True(t, found)
		require.True(t, escrow.Settled)
		replacement, found := testApp.InferenceKeeper.GetDevshardEscrow(testApp.NewContext(true), 2)
		require.True(t, found, "creating a replacement and settling an older escrow in one transaction must remain supported")
		require.False(t, replacement.Settled)
		require.Len(t, replacement.Slots, 16)
	})
}

func devshardBlockHashSettlement(t *testing.T, creator string, attacker *secp256k1.PrivKey, votes int) *inferencetypes.MsgSettleDevshardEscrow {
	t.Helper()
	const version = "block-hash-test"
	hostHash, err := keeper.ComputeDevshardHostStatsHash(nil)
	require.NoError(t, err)
	versionHash := sha256.Sum256([]byte(version))
	restHash := bytes.Repeat([]byte{0x11}, 32)
	rootInput := append(append([]byte(nil), hostHash...), make([]byte, 8)...)
	rootInput = append(rootInput, restHash...)
	rootInput = append(rootInput, versionHash[:]...)
	rootInput = append(rootInput, keeper.DevshardSettlementPhase)
	root := sha256.Sum256(rootInput)
	content := &inferencetypes.DevshardStateSignatureContent{StateRoot: root[:], EscrowId: "1", Nonce: 0}
	contentBytes, err := content.XXX_Marshal(nil, true)
	require.NoError(t, err)
	sigHash := sha256.Sum256(contentBytes)
	compact := dcrdecdsa.SignCompact(dcrdsecp.PrivKeyFromBytes(attacker.Bytes()), sigHash[:], false)
	sig := append(append([]byte(nil), compact[1:]...), compact[0]-27)
	signatures := make([]*inferencetypes.DevshardSlotSignature, votes)
	for i := range signatures {
		signatures[i] = &inferencetypes.DevshardSlotSignature{SlotId: uint32(i), Signature: sig}
	}
	return &inferencetypes.MsgSettleDevshardEscrow{Settler: creator, EscrowId: 1, StateRoot: root[:], RestHash: restHash, StateRootAndProtocolVersion: version, Signatures: signatures}
}
