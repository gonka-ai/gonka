// Package filtercore holds primitives shared between paramvalidators (top-level
// request fields) and messagevalidators (per-message shape rules). Both layers
// dispatch behavior on the routed model id; the dispatch primitive lives here
// so the two packages do not reimplement it.
package filtercore

import "slices"

// MatchesModel reports whether routedModel equals any entry in models. Empty
// models slice always returns false.
func MatchesModel(routedModel string, models []string) bool {
	return slices.Contains(models, routedModel)
}
