package keeper_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	"testing"
)

func TrainingTaskTest(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	_ = keeper
	_ = ctx
}
