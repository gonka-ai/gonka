package run

import "trainshard/internal/domain/shared/vo"

func Plan(d Desired, o Observed) []Action {
	if !d.Reserved || !d.Active {
		return CleanupPlan(o)
	}

	actions := make([]Action, 0, 6)
	if !o.HasImage(d.BaseImage) {
		actions = append(actions, Action{Kind: ActionPullImage, Image: d.BaseImage})
	}
	if !o.Drained || o.ForeignGPUWork {
		return append(actions, Action{Kind: ActionDrainNode})
	}
	if !o.MeshKey {
		actions = append(actions, Action{Kind: ActionCreateMeshIdentity})
	}
	if d.MeshConfigured && !o.MeshUp {
		actions = append(actions, Action{Kind: ActionApplyMeshConfig})
	}
	if d.Run.IsZero() {
		return actions
	}
	if !o.HasImage(d.Run.Image) {
		actions = append(actions, Action{Kind: ActionPullImage, Image: d.Run.Image})
	}

	container := o.Container
	if o.ContainerImage != d.Run.Image {
		if container.Running() {
			if !d.Start {
				actions = append(actions, Action{Kind: ActionStopContainer})
			}
			return actions
		}
		kind := ActionCreateContainer
		if container.Exists() {
			kind = ActionReplaceContainer
		}
		actions = append(actions, Action{Kind: kind, Image: d.Run.Image})
		container = vo.ContainerCreated
	}

	switch {
	case d.Start && container == vo.ContainerCreated:
		actions = append(actions, Action{Kind: ActionStartContainer})
	case !d.Start && container.Running():
		actions = append(actions, Action{Kind: ActionStopContainer})
	}
	return actions
}

func Prepared(d Desired, o Observed) bool {
	return d.Reserved && o.Drained && !o.ForeignGPUWork && o.HasImage(d.BaseImage) && o.MeshKey
}
