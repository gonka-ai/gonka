package inference

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

// ExpireInferencesForTest exposes the unexported expireInferences method for
// black-box tests in package inference_test. It is only compiled during tests
// (file ends in _test.go) and does not appear in the public API.
func (am AppModule) ExpireInferencesForTest(
	ctx context.Context,
	timeouts []types.InferenceTimeout,
	blockHeight int64,
	currentEpoch *types.Epoch,
	params *types.Params,
) error {
	return am.expireInferences(ctx, timeouts, blockHeight, currentEpoch, params)
}
