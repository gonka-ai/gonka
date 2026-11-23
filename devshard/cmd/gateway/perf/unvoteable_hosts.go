package perf

// UnvoteableSource names the hosts of a model whose slots no timeout vote can reach. It is read under the tracker's lock, so it must not call back into the tracker.
type UnvoteableSource interface {
	UnvoteableHosts(model string) map[string]bool
}

// SetUnvoteableSource binds where a remembered ejection rung reads the model's unvoteable hosts from; call it before the tracker is shared. Without one no host is unvoteable.
func (t *Tracker) SetUnvoteableSource(source UnvoteableSource) {
	t.unvoteableSource = source
}

func (t *Tracker) isUnvoteable(model, participant string) bool {
	return t.unvoteableSource != nil && t.unvoteableSource.UnvoteableHosts(model)[participant]
}
