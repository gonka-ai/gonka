package keeper

import (
	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"
)

const classicInferenceDeprecatedMessage = "classic inference is deprecated; use devshard"

func classicInferenceDeprecatedError() error {
	return sdkerrors.Wrap(types.ErrDeprecated, classicInferenceDeprecatedMessage)
}
