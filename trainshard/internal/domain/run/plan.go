package run

import (
	"strings"

	"trainshard/internal/domain/shared/vo"
)

func Plan(d Desired, o Observed) []Action {
	if !d.Reserved || !d.Active {
		return CleanupPlan(d, o)
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
	// A container is built with the rank the peer list gives it, so it cannot exist before one
	if !d.MeshConfigured {
		return actions
	}

	container := o.Container
	if o.ContainerImage != d.Run.Image || o.ContainerRevision != d.Revision {
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

func Unprepared(d Desired, o Observed) string {
	if !d.Reserved {
		return "not reserved"
	}
	waiting := make([]string, 0, 4)
	if !o.Drained {
		waiting = append(waiting, "node not drained from inference")
	}
	if o.ForeignGPUWork {
		waiting = append(waiting, "foreign work on the gpus")
	}
	if !o.HasImage(d.BaseImage) {
		waiting = append(waiting, "base image not pulled")
	}
	if !o.MeshKey {
		waiting = append(waiting, "no mesh identity")
	}
	return strings.Join(waiting, ", ")
}
