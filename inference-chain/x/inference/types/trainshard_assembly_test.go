package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestTrainshardAssemblyWaitsOutPoCAndConfirmationPoC(t *testing.T) {
	params := *types.DefaultEpochParams()
	params.EpochLength = 400
	epoch := types.Epoch{Index: 3, PocStartBlockHeight: 100}
	endOfPoC := int64(100 + 10 + 2 + 6)
	confirmation := func(phase types.ConfirmationPoCPhase) *types.ConfirmationPoCEvent {
		return &types.ConfirmationPoCEvent{EpochIndex: 3, GenerationStartHeight: 200, Phase: phase}
	}
	endOfConfirmation := int64(200 + 10 - 1 + 2 + 2 + 6 - 1)

	cases := []struct {
		name   string
		height int64
		epoch  types.Epoch
		event  *types.ConfirmationPoCEvent
		want   int64
	}{
		{"before PoC", 99, epoch, nil, 99},
		{"PoC starts", 100, epoch, nil, endOfPoC},
		{"PoC validates", endOfPoC - 1, epoch, nil, endOfPoC},
		{"PoC is over", endOfPoC, epoch, nil, endOfPoC},
		{"the next PoC starts before its epoch is stored", 500, epoch, nil, 500 + 18},
		{"the genesis epoch hands over to the first PoC", 400, types.Epoch{Index: 0}, nil, 400 + 18},
		{"the genesis epoch runs no PoC", 5, types.Epoch{Index: 0}, nil, 5},
		{"confirmation PoC in its grace period", 195, epoch, confirmation(types.ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD), endOfConfirmation + 1},
		{"confirmation PoC generates", 205, epoch, confirmation(types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION), endOfConfirmation + 1},
		{"confirmation PoC validates", endOfConfirmation, epoch, confirmation(types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION), endOfConfirmation + 1},
		{"confirmation PoC completed", endOfConfirmation + 2, epoch, confirmation(types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED), endOfConfirmation + 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, types.TrainshardAssemblyOpensAt(tc.height, tc.epoch, params, tc.event))
		})
	}
}
