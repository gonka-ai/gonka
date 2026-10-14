package poc

import "github.com/productscience/inference/x/inference/types"

// SchemeForStage is freeze-time dispatch. After freeze, callers must use the stored recipe.
func SchemeForStage(p *types.PocParams, event *types.ConfirmationPoCEvent) types.PocScheme {
	return types.SchemeForStage(p, event)
}

func IsSchemeTracking(p *types.PocParams, event *types.ConfirmationPoCEvent) bool {
	return types.IsSchemeTracking(p, event)
}

func DecodeMaxForStage(scheme types.PocScheme, decodeMaxTokens int64) int64 {
	return types.DecodeMaxForStage(scheme, decodeMaxTokens)
}
