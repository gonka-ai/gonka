package run

// WipePlan removes everything a run left on the machine, always in the same order
func WipePlan(o Observed) []Action {
	actions := make([]Action, 0, 5)
	if o.Container.Running() {
		actions = append(actions, Action{Kind: ActionStopContainer})
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
	if o.TrainingProcesses {
		actions = append(actions, Action{Kind: ActionKillGPUProcesses})
	}
	return actions
}

// CleanupPlan wipes the run, then lets go of the shard: a node taken out of inference goes
// back, one that never left is only forgotten, and either way the state stops pinning the shard
func CleanupPlan(d Desired, o Observed) []Action {
	if actions := WipePlan(o); len(actions) > 0 {
		return actions
	}
	switch {
	case o.Drained:
		return []Action{{Kind: ActionReturnNode}}
	case !d.Shard.IsZero():
		return []Action{{Kind: ActionForgetRun}}
	}
	return nil
}
