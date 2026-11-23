package app_test

import (
	"math/rand"
	"testing"
	"time"

	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// A proposer need not run our CheckTx. The consensus fee checker must enforce
// the enabled duty group's minimum even when a signed, ineligible claim goes
// directly into FinalizeBlock.
func TestNetworkDuty_DeliverTx_ChargesIneligibleClaim(t *testing.T) {
	for _, paid := range []bool{false, true} {
		name := "zero fee rejected by ante"
		if paid {
			name = "paid fee retained when handler rejects"
		}
		t.Run(name, func(t *testing.T) {
			f := setupDutyCheckTx(t)
			header := cmtproto.Header{Height: f.testApp.LastBlockHeight(), ChainID: TallyTestChainID, Time: time.Now().UTC()}
			ctx := f.testApp.NewUncachedContext(false, header)
			params, err := f.testApp.InferenceKeeper.GetParams(ctx)
			require.NoError(t, err)
			params.FeeParams = inferencetypes.DefaultFeeParams()
			params.FeeParams.EnabledFeeGroups = []string{inferencetypes.FeeGroupEpoch}
			params.FeeParams.GroupByName(inferencetypes.FeeGroupEpoch).MinGasPrice = 10
			require.NoError(t, f.testApp.InferenceKeeper.SetParams(ctx, params))
			_, err = f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: f.testApp.LastBlockHeight() + 1, Time: time.Now().UTC()})
			require.NoError(t, err)
			_, err = f.testApp.Commit()
			require.NoError(t, err)

			checkCtx := f.testApp.NewContext(true)
			acc := f.testApp.AccountKeeper.GetAccount(checkCtx, f.registered)
			before := f.testApp.BankKeeper.GetBalance(checkCtx, f.registered, inferencetypes.BaseCoin).Amount
			fees := sdk.NewCoins()
			if paid {
				fees = sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewIntFromUint64(simtestutil.DefaultGenTxGas).MulRaw(10)))
			}
			tx, err := simtestutil.GenSignedMockTx(
				rand.New(rand.NewSource(1)), f.testApp.TxConfig(),
				[]sdk.Msg{claimRewardsFor(f.registered)}, fees, simtestutil.DefaultGenTxGas,
				TallyTestChainID, []uint64{acc.GetAccountNumber()}, []uint64{acc.GetSequence()}, f.registeredPriv,
			)
			require.NoError(t, err)
			txBytes, err := f.testApp.TxConfig().TxEncoder()(tx)
			require.NoError(t, err)
			response, err := f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
				Height: f.testApp.LastBlockHeight() + 1, Time: time.Now().UTC(), Txs: [][]byte{txBytes},
			})
			require.NoError(t, err)
			require.Len(t, response.TxResults, 1)
			result := response.TxResults[0]
			if paid {
				require.Equal(t, inferencetypes.ErrActiveParticipantNotFound.ABCICode(), result.Code, result.Log)
				require.Positive(t, result.GasUsed)
				require.LessOrEqual(t, result.GasUsed, int64(simtestutil.DefaultGenTxGas))
			} else {
				require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), result.Code, result.Log)
			}
			_, err = f.testApp.Commit()
			require.NoError(t, err)
			after := f.testApp.BankKeeper.GetBalance(f.testApp.NewContext(true), f.registered, inferencetypes.BaseCoin).Amount
			require.Equal(t, before.Sub(fees.AmountOf(inferencetypes.BaseCoin)), after)
		})
	}
}
