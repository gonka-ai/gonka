package v0_2_18

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

const ics27Version = `{"version":"ics27-1","controller_connection_id":"connection-localhost","host_connection_id":"connection-localhost","encoding":"proto3","tx_type":"sdk_multi_msg"}`

func TestUnwrapICS29Version(t *testing.T) {
	quoted, err := json.Marshal(ics27Version)
	require.NoError(t, err)
	wrapped := `{"fee_version":"ics29-1","app_version":` + string(quoted) + `}`

	got, ok, err := unwrapICS29Version(wrapped)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ics27Version, got)

	got, ok, err = unwrapICS29Version("ics20-1")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, got)

	got, ok, err = unwrapICS29Version(ics27Version)
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, got)

	_, _, err = unwrapICS29Version(`{"fee_version":"ics29-2","app_version":"ics20-1"}`)
	require.ErrorContains(t, err, "unsupported ICS-29 fee_version")

	_, _, err = unwrapICS29Version(`{"fee_version":"ics29-1"}`)
	require.ErrorContains(t, err, "missing app_version")

	_, _, err = unwrapICS29Version(`{"fee_version":"ics29-1","app_version":""}`)
	require.ErrorContains(t, err, "empty app_version")
}

func TestUnwrapICS29ChannelVersions(t *testing.T) {
	wrapped := `{"fee_version":"ics29-1","app_version":"ics20-1"}`
	keeper := &fakeChannelKeeper{channels: []channeltypes.IdentifiedChannel{
		channelEnd("transfer", "channel-0", "ics20-1"),
		channelEnd("icahost", "channel-4", ics27Version),
		channelEnd("icacontroller-gonka1test", "channel-6", wrapped),
		channelEnd("icahost", "channel-7", wrapped),
	}}

	require.NoError(t, unwrapICS29ChannelVersions(sdk.Context{}, keeper, discardLogger{}))

	_, rewrittenTransfer := keeper.saved["transfer/channel-0"]
	require.False(t, rewrittenTransfer)
	_, rewritten27 := keeper.saved["icahost/channel-4"]
	require.False(t, rewritten27)
	require.Equal(t, "ics20-1", keeper.saved["icacontroller-gonka1test/channel-6"].Version)
	require.Equal(t, "ics20-1", keeper.saved["icahost/channel-7"].Version)
	require.Equal(t, channeltypes.OPEN, keeper.saved["icahost/channel-7"].State)
	require.Equal(t, []string{"connection-localhost"}, keeper.saved["icahost/channel-7"].ConnectionHops)
}

func TestUnwrapICS29ChannelVersionsRejectsBadMetadata(t *testing.T) {
	keeper := &fakeChannelKeeper{channels: []channeltypes.IdentifiedChannel{
		channelEnd("transfer", "channel-9", `{"fee_version":"ics29-9","app_version":"ics20-1"}`),
	}}

	err := unwrapICS29ChannelVersions(sdk.Context{}, keeper, discardLogger{})
	require.ErrorContains(t, err, "channel-9")
	require.Empty(t, keeper.saved)
}

func TestSweepFeeIBCBalance(t *testing.T) {
	bank := &fakeBank{balance: sdk.NewCoins(sdk.NewInt64Coin("ngonka", 25))}
	distr := &fakeDistr{}

	require.NoError(t, sweepFeeIBCBalance(context.Background(), bank, distr, discardLogger{}))
	require.Equal(t, "25ngonka", distr.amount.String())
	require.True(t, distr.sender.Equals(authtypes.NewModuleAddress(feeIBCModuleName)))

	bank.balance = sdk.NewCoins()
	distr.amount = nil
	distr.sender = nil
	require.NoError(t, sweepFeeIBCBalance(context.Background(), bank, distr, discardLogger{}))
	require.Nil(t, distr.sender)
}

type fakeChannelKeeper struct {
	channels []channeltypes.IdentifiedChannel
	saved    map[string]channeltypes.Channel
}

func (f *fakeChannelKeeper) GetAllChannels(sdk.Context) []channeltypes.IdentifiedChannel {
	return f.channels
}

func (f *fakeChannelKeeper) SetChannel(_ sdk.Context, portID, channelID string, channel channeltypes.Channel) {
	if f.saved == nil {
		f.saved = map[string]channeltypes.Channel{}
	}
	f.saved[portID+"/"+channelID] = channel
}

func channelEnd(portID, channelID, version string) channeltypes.IdentifiedChannel {
	return channeltypes.IdentifiedChannel{
		State:          channeltypes.OPEN,
		Ordering:       channeltypes.UNORDERED,
		ConnectionHops: []string{"connection-localhost"},
		Version:        version,
		PortId:         portID,
		ChannelId:      channelID,
	}
}

type fakeBank struct {
	balance sdk.Coins
}

func (f *fakeBank) GetAllBalances(context.Context, sdk.AccAddress) sdk.Coins {
	return f.balance
}

type fakeDistr struct {
	amount sdk.Coins
	sender sdk.AccAddress
}

func (f *fakeDistr) FundCommunityPool(_ context.Context, amount sdk.Coins, sender sdk.AccAddress) error {
	f.amount = amount
	f.sender = sender
	return nil
}

type discardLogger struct{}

func (discardLogger) LogInfo(string, types.SubSystem, ...interface{}) {}
