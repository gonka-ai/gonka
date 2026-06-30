package paramvalidators

// ValidatorContext is the input bundle passed to DocumentValidator.Validate. Document is
// shared with the pipeline -- validators may mutate it (e.g. per-model mirrors).
type ValidatorContext struct {
	Document    map[string]any
	RoutedModel string
}

// ParameterContext is the input bundle passed to ParameterHandler.HandleParameter.
// Document is shared with the pipeline; handlers may mutate it (delete the field,
// rewrite its value, etc.). Parameter is the field name the handler is wired against.
type ParameterContext struct {
	Document    map[string]any
	Parameter   string
	RoutedModel string
}

// ParameterHandler is the leaf type for stage-driven rewrites of a single field.
// The caller wraps the returned error as HTTP 400.
type ParameterHandler interface {
	HandleParameter(ParameterContext) error
}
