package run

// WipePlan kills a leftover process before its container is removed: the container is all that
// says the process is the run's
func WipePlan(o Observed) []Action {
	actions := make([]Action, 0, 5)
	if o.Container.Running() {
		actions = append(actions, Action{Kind: ActionStopContainer})
	}
	if o.TrainingProcesses {
		actions = append(actions, Action{Kind: ActionKillGPUProcesses})
	}
	if o.Container.Exists() {
		actions = append(actions, Action{Kind: ActionRemoveContainer})
	}
	if o.MeshKey || o.MeshUp {
		actions = append(actions, Action{Kind: ActionRemoveMesh})
	}
	if o.VolumesPresent {
		actions = append(actions, Action{Kind: ActionWipeVolumes})
	}
	return actions
}

func CleanupPlan(d Desired, o Observed) []Action {
	if actions := WipePlan(o); len(actions) > 0 {
		return actions
	}
	// a drained node with no run behind it is the operator's, and is left alone
	if d.Shard.IsZero() {
		return nil
	}
	// a node already reserved for the next shard stays drained for it: a return in between would
	// load a model only to unload it again
	if d.Handover {
		return []Action{{Kind: ActionForgetRun}}
	}
	return []Action{{Kind: ActionReturnNode}}
}
