package chain

func (o *PhaseObserver) publishWithPreviousParticipants(snapshot, previous PhaseSnapshot, lastError string) {
	snapshot.CurrentWeights = previous.CurrentWeights
	snapshot.FullWeights = previous.FullWeights
	snapshot.CurrentWeightsByModel = previous.CurrentWeightsByModel
	snapshot.FullWeightsByModel = previous.FullWeightsByModel
	snapshot.Preserved = previous.Preserved
	snapshot.PreservedByModel = previous.PreservedByModel
	snapshot.InferenceURLs = previous.InferenceURLs
	snapshot.LastHealthyAt = previous.LastHealthyAt
	snapshot.LastError = joinSnapshotError(snapshot.LastError, lastError)
	o.publish(snapshot)
}

func (o *PhaseObserver) Snapshot() PhaseSnapshot {
	return *o.current.Load()
}

// Subscribe registers a callback invoked synchronously on every publish. See README.md, "The poll loop".
func (o *PhaseObserver) Subscribe(cb func(PhaseSnapshot)) (cancel func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id := o.nextID
	o.nextID++
	o.subscribers[id] = cb
	return func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		delete(o.subscribers, id)
	}
}

// SetNarrator binds the journal the health edge is written through; call it before Start.
func (o *PhaseObserver) SetNarrator(narrator healthNarrator) {
	o.narrator = narrator
}

// publish stores snapshot then synchronously notifies the current subscribers, in no particular order.
func (o *PhaseObserver) publish(snapshot PhaseSnapshot) {
	if snapshot.LastHealthyAt.IsZero() {
		if previous := o.current.Load(); previous != nil {
			snapshot.LastHealthyAt = previous.LastHealthyAt
		}
	}
	o.current.Store(&snapshot)
	o.narrateHealth(snapshot)
	o.mu.Lock()
	callbacks := make([]func(PhaseSnapshot), 0, len(o.subscribers))
	for _, cb := range o.subscribers {
		callbacks = append(callbacks, cb)
	}
	o.mu.Unlock()
	for _, cb := range callbacks {
		cb(snapshot)
	}
}
