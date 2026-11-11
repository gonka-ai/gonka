package v0_2_18

import (
	"context"
	"testing"

	"cosmossdk.io/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	blstypes "github.com/productscience/inference/x/bls/types"
)

type fakeBeaconKeeper struct {
	signing, active       uint64
	hasSigning, hasActive bool
	activated             bool
	gotExtFrom            uint64
	gotCandidates         []uint64
	params                blstypes.Params
}

func (f *fakeBeaconKeeper) ActivateBeacon(_ sdk.Context, extFrom uint64, c []uint64) bool {
	if f.activated {
		return false
	}
	f.activated, f.gotExtFrom, f.gotCandidates = true, extFrom, c
	return true
}
func (f *fakeBeaconKeeper) GetCurrentSigningEpochID(sdk.Context) (uint64, bool) {
	return f.signing, f.hasSigning
}
func (f *fakeBeaconKeeper) GetActiveEpochID(sdk.Context) (uint64, bool)        { return f.active, f.hasActive }
func (f *fakeBeaconKeeper) GetParams(context.Context) (blstypes.Params, error) { return f.params, nil }
func (f *fakeBeaconKeeper) SetParams(_ context.Context, p blstypes.Params) error {
	f.params = p
	return nil
}

func beaconCtxAt(h int64) sdk.Context {
	return sdk.NewContext(nil, cmtproto.Header{Height: h}, false, log.NewNopLogger())
}

func TestActivateBlockBeacon(t *testing.T) {
	k := &fakeBeaconKeeper{signing: 10, hasSigning: true, active: 11, hasActive: true, params: blstypes.DefaultParams()}
	k.params.BeaconKeySwitchDelay = 0 // as stored before this release (field absent)

	activated, err := ActivateBlockBeacon(beaconCtxAt(500), k)
	require.NoError(t, err)
	require.True(t, activated)
	require.Equal(t, uint64(500), k.gotExtFrom, "ext_from = upgrade height")
	require.ElementsMatch(t, []uint64{11, 10, 9, 8}, k.gotCandidates, "signing epoch ±, and the running DKG")
	require.Equal(t, blstypes.DefaultBeaconKeySwitchDelay, k.params.BeaconKeySwitchDelay)

	// Second run: nothing changes, not even a governance-changed delay.
	k.params.BeaconKeySwitchDelay = 9
	activated, err = ActivateBlockBeacon(beaconCtxAt(600), k)
	require.NoError(t, err)
	require.False(t, activated)
	require.Equal(t, uint64(500), k.gotExtFrom)
	require.Equal(t, uint64(9), k.params.BeaconKeySwitchDelay)
}

func TestBeaconKeyCandidatesEdges(t *testing.T) {
	require.Empty(t, beaconKeyCandidates(beaconCtxAt(1), &fakeBeaconKeeper{}), "fresh chain state")
	require.ElementsMatch(t, []uint64{2, 1}, beaconKeyCandidates(beaconCtxAt(1), &fakeBeaconKeeper{signing: 1, hasSigning: true}))
	_, err := ActivateBlockBeacon(beaconCtxAt(0), &fakeBeaconKeeper{})
	require.Error(t, err)
}
