package app

import (
	"errors"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type fakeBLSMembership struct {
	members map[uint64][]string // epoch -> members; absent epoch = no BLS data
	request map[string]uint64
	err     error
	asked   []uint64
}

func (f *fakeBLSMembership) IsEpochBLSParticipant(_ sdk.Context, epochID uint64, addr string) (bool, error) {
	f.asked = append(f.asked, epochID)
	if f.err != nil {
		return false, f.err
	}
	members, ok := f.members[epochID]
	if !ok {
		return false, blstypes.ErrEpochBLSDataNotFound
	}
	for _, m := range members {
		if m == addr {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeBLSMembership) SigningRequestEpoch(_ sdk.Context, requestID []byte) (uint64, error) {
	epoch, ok := f.request[string(requestID)]
	if !ok {
		return 0, blstypes.ErrSigningRequestNotFound
	}
	return epoch, nil
}

// Each BLS duty is checked against the epoch its handler reads.
func TestBLSDutyMember_EpochPerMessage(t *testing.T) {
	const a = "member"
	cases := []struct {
		name  string
		msg   sdk.Msg
		asked []uint64
	}{
		{"dealer part", &blstypes.MsgSubmitDealerPart{EpochId: 10}, []uint64{10}},
		{"verification vector", &blstypes.MsgSubmitVerificationVector{EpochId: 10}, []uint64{10}},
		{"dealer complaints", &blstypes.MsgRespondDealerComplaints{EpochId: 10}, []uint64{10}},
		{"group key validation", &blstypes.MsgSubmitGroupKeyValidationSignature{NewEpochId: 11}, []uint64{10}},
		{"partial signature", &blstypes.MsgSubmitPartialSignature{RequestId: []byte("r")}, []uint64{10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bk := &fakeBLSMembership{
				members: map[uint64][]string{10: {a}, 11: {}},
				request: map[string]uint64{"r": 10},
			}
			ok, err := blsDutyMember(sdk.Context{}, bk, tc.msg, a)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, tc.asked, bk.asked)
		})
	}
}

func TestBLSDutyMember_GroupKeyValidationGenesisEpoch(t *testing.T) {
	bk := &fakeBLSMembership{members: map[uint64][]string{1: {"m"}}}
	ok, err := blsDutyMember(sdk.Context{}, bk, &blstypes.MsgSubmitGroupKeyValidationSignature{NewEpochId: 1}, "m")
	require.NoError(t, err)
	require.False(t, ok, "the handler rejects epoch 1 outright")
}

// A read error other than "not found" must not keep a member out.
func TestCheckBLSMember_StoreErrorPasses(t *testing.T) {
	d := NetworkDutySignerDecorator{blsKeeper: &fakeBLSMembership{err: errors.New("store")}}
	require.NoError(t, d.checkBLSMember(sdk.Context{}, &blstypes.MsgSubmitDealerPart{EpochId: 10}, "m"))
}

// The fee waiver applies the same DKG membership as CheckTx; any read error
// or a missing keeper withholds it.
func TestBLSDutyWaiver_RequiresMembership(t *testing.T) {
	actor := dutyActor()
	ik, ctx := registeredInactiveDutyKeeper(t, actor)
	member := &blstypes.MsgSubmitDealerPart{Creator: actor.String(), EpochId: 10}
	other := &blstypes.MsgSubmitDealerPart{Creator: actor.String(), EpochId: 11}
	bk := &fakeBLSMembership{members: map[uint64][]string{10: {actor.String()}, 11: {}}}

	require.True(t, isAuthorizedNetworkDuty(ctx, member, ik, bk), "registered DKG member keeps the waiver")
	require.False(t, isAuthorizedNetworkDuty(ctx, other, ik, bk), "registered non-member loses the waiver")
	require.False(t, isAuthorizedNetworkDuty(ctx, member, ik, nil), "no BLS keeper fails closed")
	require.False(t, isAuthorizedNetworkDuty(ctx, member, ik, &fakeBLSMembership{err: errors.New("store")}),
		"a read error withholds the waiver")
	require.True(t, isAuthorizedNetworkDuty(ctx, &inferencetypes.MsgSubmitSeed{Creator: actor.String()}, ik, nil),
		"non-BLS duties keep the registration predicate")
}
