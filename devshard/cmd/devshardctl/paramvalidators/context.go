package paramvalidators

// ValidatorContext is the input bundle passed to DocumentValidator.Validate. Document is
// shared with the pipeline -- validators may mutate it (e.g. per-model mirrors).
type ValidatorContext struct {
	Document    map[string]any
	RoutedModel string
}
