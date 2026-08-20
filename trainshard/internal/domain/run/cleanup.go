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

// CleanupPlan wipes the run and hands the node back once nothing of it is left
func CleanupPlan(o Observed) []Action {
	actions := WipePlan(o)
	if len(actions) == 0 && o.Drained {
		actions = append(actions, Action{Kind: ActionReturnNode})
	}
	return actions
}
