package run

// WipePlan removes everything a run left on the machine, always in the same order; a leftover
// process is only known to be the run's by the container it came from, so it goes first
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

// CleanupPlan asks for the handback until it goes through. A drained node with no run is left alone.
func CleanupPlan(d Desired, o Observed) []Action {
	if actions := WipePlan(o); len(actions) > 0 {
		return actions
	}
	if d.Shard.IsZero() {
		return nil
	}
	return []Action{{Kind: ActionReturnNode}}
}
