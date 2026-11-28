package app

import (
	"context"
	"errors"
	"testing"

	"cosmossdk.io/log/v2"
	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	protov2 "google.golang.org/protobuf/proto"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// feeTransferGas stands in for the KV cost of the fee collector transfer
// (#2300 measured 24,072 gas on gonka-testnet-4).
const feeTransferGas = 24_072

type feeSend struct {
	from   sdk.AccAddress
	module string
	amt    sdk.Coins
}

// meteringBank charges feeTransferGas per fee transfer, like the real bank
// keeper's balance reads and writes, and records each call.
type meteringBank struct {
	sends []feeSend
	err   error
}

func (b *meteringBank) IsSendEnabledCoins(context.Context, ...sdk.Coin) error { return nil }
func (b *meteringBank) SendCoins(context.Context, sdk.AccAddress, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (b *meteringBank) SendCoinsFromAccountToModule(ctx context.Context, from sdk.AccAddress, module string, amt sdk.Coins) error {
	sdk.UnwrapSDKContext(ctx).GasMeter().ConsumeGas(feeTransferGas, "test fee transfer")
	b.sends = append(b.sends, feeSend{from: from, module: module, amt: amt})
	return b.err
}

var _ authtypes.BankKeeper = (*meteringBank)(nil)

// feeAccounts knows every address and the fee collector; only the two methods
// DeductFeeDecorator and FeeDeductSimGasDecorator call are implemented.
type feeAccounts struct {
	ante.AccountKeeper
}

func (feeAccounts) GetAccount(_ context.Context, addr sdk.AccAddress) sdk.AccountI {
	return authtypes.NewBaseAccountWithAddress(addr)
}

func (feeAccounts) GetModuleAddress(name string) sdk.AccAddress {
	return authtypes.NewModuleAddress(name)
}

type simFeeTx struct {
	fee     sdk.Coins
	gas     uint64
	payer   sdk.AccAddress
	granter sdk.AccAddress
}

func (t simFeeTx) GetMsgs() []sdk.Msg                    { return nil }
func (t simFeeTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }
func (t simFeeTx) GetFee() sdk.Coins                     { return t.fee }
func (t simFeeTx) GetGas() uint64                        { return t.gas }
func (t simFeeTx) FeePayer() []byte                      { return t.payer }
func (t simFeeTx) FeeGranter() []byte {
	if t.granter == nil {
		return nil
	}
	return t.granter
}

var (
	simPayer   = sdk.AccAddress("sim-fee-payer-______")
	simGranter = sdk.AccAddress("sim-fee-granter-____")
)

func meteredContext() sdk.Context {
	return sdk.NewContext(nil, cmtproto.Header{Height: 100}, false, log.NewNopLogger()).
		WithGasMeter(storetypes.NewInfiniteGasMeter())
}

func passThrough(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) { return ctx, nil }

// feeChain is the production order: DeductFeeDecorator, then the Simulate meter.
func feeChain(bank *meteringBank) sdk.AnteHandler {
	paidChecker := func(ctx sdk.Context, tx sdk.Tx) (sdk.Coins, int64, error) {
		return tx.(sdk.FeeTx).GetFee(), 0, nil
	}
	return sdk.ChainAnteDecorators(
		ante.NewDeductFeeDecorator(feeAccounts{}, bank, nil, paidChecker),
		NewFeeDeductSimGasDecorator(feeAccounts{}, bank),
	)
}

// TestFeeDeductSimGas_SimulateMatchesFinalizeGas reproduces #2300: `--gas auto`
// simulates with a zero fee and Finalize pays gas x price. Without the decorator
// the Simulate run skips the fee transfer and comes out feeTransferGas short.
func TestFeeDeductSimGas_SimulateMatchesFinalizeGas(t *testing.T) {
	const gas = 70_335
	paid := sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, gas)) // 1ngonka x gas

	finalizeBank := &meteringBank{}
	finalizeCtx := meteredContext()
	_, err := feeChain(finalizeBank)(finalizeCtx, simFeeTx{fee: paid, gas: gas, payer: simPayer}, false)
	require.NoError(t, err)

	simBank := &meteringBank{}
	simCtx := meteredContext()
	_, err = feeChain(simBank)(simCtx, simFeeTx{fee: sdk.Coins{}, gas: 0, payer: simPayer}, true)
	require.NoError(t, err)

	require.Len(t, finalizeBank.sends, 1)
	require.Len(t, simBank.sends, 1, "Simulate must meter one fee transfer for a zero-fee tx")
	require.Equal(t, finalizeCtx.GasMeter().GasConsumed(), simCtx.GasMeter().GasConsumed(),
		"Simulate gas must match Finalize gas for the fee step")
	require.Equal(t, simPayer, simBank.sends[0].from)
	require.Equal(t, authtypes.FeeCollectorName, simBank.sends[0].module)
}

func TestFeeDeductSimGas_ChargesGranterWhenSet(t *testing.T) {
	bank := &meteringBank{}
	dec := NewFeeDeductSimGasDecorator(feeAccounts{}, bank)
	_, err := dec.AnteHandle(meteredContext(), simFeeTx{payer: simPayer, granter: simGranter}, true, passThrough)
	require.NoError(t, err)
	require.Len(t, bank.sends, 1)
	require.Equal(t, simGranter, bank.sends[0].from)
}

func TestFeeDeductSimGas_SkipsNonZeroFee(t *testing.T) {
	bank := &meteringBank{}
	_, err := feeChain(bank)(meteredContext(), simFeeTx{
		fee: sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 5)), gas: 5, payer: simPayer,
	}, true)
	require.NoError(t, err)
	require.Len(t, bank.sends, 1, "a non-zero simulated fee is already deducted once by the SDK")
}

func TestFeeDeductSimGas_SkipsWhenNotSimulate(t *testing.T) {
	bank := &meteringBank{}
	dec := NewFeeDeductSimGasDecorator(feeAccounts{}, bank)
	_, err := dec.AnteHandle(meteredContext(), simFeeTx{payer: simPayer}, false, passThrough)
	require.NoError(t, err)
	require.Empty(t, bank.sends)
}

func TestFeeDeductSimGas_SkipsFeeBypassedTx(t *testing.T) {
	bank := &meteringBank{}
	dec := NewFeeDeductSimGasDecorator(feeAccounts{}, bank)
	ctx := meteredContext().WithValue(networkDutyFeeBypassKey{}, true)
	_, err := dec.AnteHandle(ctx, simFeeTx{payer: simPayer}, true, passThrough)
	require.NoError(t, err)
	require.Empty(t, bank.sends, "network-duty and pool-swap bypassed txs pay no fee in Finalize")
}

func TestFeeDeductSimGas_IgnoresTransferError(t *testing.T) {
	bank := &meteringBank{err: errors.New("insufficient funds")}
	dec := NewFeeDeductSimGasDecorator(feeAccounts{}, bank)
	nextCalled := false
	_, err := dec.AnteHandle(meteredContext(), simFeeTx{payer: simPayer}, true, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err, "gas estimation must not fail on the stand-in transfer")
	require.True(t, nextCalled)
	require.Len(t, bank.sends, 1)
}
