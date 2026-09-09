package store_test

import (
	"testing"

	"devshard/testenv/mockchain/store"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPatchDevshardEscrowParams_PublishesAtChainTip(t *testing.T) {
	st := store.New()
	st.BlockHeight = 150
	st.ParamsBlockHeight = 150
	st.Epoch = inferencetypes.Epoch{Index: 1, PocStartBlockHeight: 100}
	st.Params = inferencetypes.Params{
		DevshardEscrowParams: &inferencetypes.DevshardEscrowParams{MaxNonce: 500},
	}
	st.PatchDevshardEscrowParams(func(p *inferencetypes.DevshardEscrowParams) {
		p.MaxNonce = 777
	})
	require.Equal(t, uint32(777), st.GetParams().DevshardEscrowParams.MaxNonce)
	require.Equal(t, int64(151), st.GetParamsBlockHeight())
	require.Equal(t, int64(100), st.GetEpoch().PocStartBlockHeight, "param-only patch must not move poc start")
}

func TestAdvanceEpochWithoutCatchUp(t *testing.T) {
	st := store.New()
	st.BlockHeight = 200
	st.Epoch = inferencetypes.Epoch{Index: 1, PocStartBlockHeight: 100}
	st.Params = inferencetypes.Params{
		EpochParams: &inferencetypes.EpochParams{EpochLength: 400},
	}
	epoch := st.AdvanceEpochWithoutCatchUp()
	require.Equal(t, uint64(2), epoch.Index)
	require.Equal(t, int64(200), epoch.PocStartBlockHeight)
	require.Equal(t, int64(600), st.GetNextPocStartBlockHeight())
}
