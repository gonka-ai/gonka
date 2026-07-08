// Package messagevalidators holds leaf-level shape/bounds validators for
// per-message content. It mirrors paramvalidators (which validates top-level
// request fields). Validators are model-agnostic — per-model dispatch happens
// upstream in ChatMessageProcessor's role-policy catalog, which picks which
// validator (if any) to call for a given (route, role) pair.
package messagevalidators

// ContentValidator validates the value of a single message's `content` field.
// Implementations are pure: they do not mutate the message and do not have
// access to cross-message state. Cross-message correlation (e.g. tool_call_id
// matching) is handled by ChatMessageProcessor's per-role policy, not here.
type ContentValidator interface {
	Validate(content any) error
}
