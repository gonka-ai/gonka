package filters

import "maps"

// ProfileSet is an immutable operator binding of models to profiles, consulted before the built-in bindings and the default preset.
type ProfileSet struct {
	byModel map[string]*Profile
}

// NewProfileSet copies byModel; a model it does not name runs its built-in preset, otherwise the default preset.
func NewProfileSet(byModel map[string]*Profile) *ProfileSet {
	return &ProfileSet{byModel: maps.Clone(byModel)}
}

// For never returns nil: a nil set is the built-in bindings and the default preset alone.
func (set *ProfileSet) For(model string) *Profile {
	if set != nil {
		if profile, bound := set.byModel[model]; bound {
			return profile
		}
	}
	return BuiltInProfileFor(model).resolved()
}
