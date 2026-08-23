package paramvalidators

// KimiMaxTokensFloorValidator lifts max_tokens and max_completion_tokens to
// Min for Kimi-K2.6. Below 16 the model emits only </think>, stripped by vLLM.
type KimiMaxTokensFloorValidator struct {
	Model string
	Min   uint64
}

func (v KimiMaxTokensFloorValidator) Validate(vctx ValidatorContext) error {
	if vctx.RoutedModel != v.Model {
		return nil
	}
	floorUintField(vctx.Document, "max_tokens", v.Min)
	floorUintField(vctx.Document, "max_completion_tokens", v.Min)
	return nil
}

func floorUintField(doc map[string]any, key string, min uint64) {
	value, ok := numericAsUint64(doc[key])
	if !ok {
		return
	}
	if value < min {
		doc[key] = min
	}
}
