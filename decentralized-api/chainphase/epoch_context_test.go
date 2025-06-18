package chainphase_test

import (
	"decentralized-api/chainphase"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func Test(t *testing.T) {
	epochParams := types.EpochParams{
		EpochLength:           100,
		EpochMultiplier:       1,
		EpochShift:            90,
		PocStageDuration:      20,
		PocExchangeDuration:   1,
		PocValidationDelay:    1,
		PocValidationDuration: 10,
	}
	epochGroup := types.EpochGroupData{
		PocStartBlockHeight: 110,
		EpochGroupId:        1,
	}

	startOfNexEpochPoc := int64(epochGroup.PocStartBlockHeight) + epochParams.EpochLength
	var i = startOfNexEpochPoc
	for i < startOfNexEpochPoc+epochParams.PocStageDuration {
		ec := chainphase.NewEpochContext(&epochGroup, epochParams, i)
		require.Equal(t, epochGroup.EpochGroupId+1, ec.Epoch)
		i++
	}
}
