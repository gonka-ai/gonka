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
	if !o.Drained || o.ForeignGPUWork {
		actions = append(actions, Action{Kind: ActionDrainNode})
	}
	if !o.HasImage(d.BaseImage) {
		actions = append(actions, Action{Kind: ActionPullImage, Image: d.BaseImage})
	}
	// an image is judged without the cards, so a deploy that lands while the dapi still lets go of
	// the node hears its refusal instead of leaving it for the loop
	if !o.Drained || o.ForeignGPUWork {
		return append(actions, checkRunImage(d, o)...)
	}
	// the key is made before the signed member is stored, so a key alone is a step to finish
	if !o.MeshKey || !o.MeshIdentity {
		actions = append(actions, Action{Kind: ActionCreateMeshIdentity})
	}
	if d.MeshConfigured && !o.MeshUp {
		actions = append(actions, Action{Kind: ActionApplyMeshConfig})
	}
	if d.Run.IsZero() {
		return actions
	}
	// A container is built with the rank the peer list gives it, so it cannot exist before one;
	// its image can already be refused, while the deploy that brought it still waits for the answer
	if !d.MeshConfigured {
		return append(actions, checkRunImage(d, o)...)
	}
	if !o.HasImage(d.Run.Image) {
		actions = append(actions, Action{Kind: ActionPullImage, Image: d.Run.Image})
	}

	// a container is boxed when it is created; a box rebuilt under one that has not started is
	// open, so the container is built again
	container := o.Container
	unboxed := container == vo.ContainerCreated && !o.Fenced
	if o.ContainerImage != d.Run.Image || o.ContainerRevision != d.Revision || unboxed {
		if container.Running() {
			if !d.Start {
				actions = append(actions, Action{Kind: ActionStopContainer})
			}
			return actions
		}
		// a finished job is not run again on a new place: only a deploy, which clears start, hands
		// out the next container
		if container == vo.ContainerExited && d.Start {
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

func checkRunImage(d Desired, o Observed) []Action {
	if d.Run.IsZero() {
		return nil
	}
	actions := make([]Action, 0, 2)
	if !o.HasImage(d.Run.Image) {
		actions = append(actions, Action{Kind: ActionPullImage, Image: d.Run.Image})
	}
	return append(actions, Action{Kind: ActionVerifyImage, Image: d.Run.Image})
}

func Prepared(d Desired, o Observed) bool {
	return d.Reserved && o.Drained && !o.ForeignGPUWork && o.HasImage(d.BaseImage) && o.MeshKey && o.MeshIdentity
}

func Unprepared(d Desired, o Observed) string {
	if !d.Reserved {
		return "not reserved"
	}
	waiting := make([]string, 0, 4)
	if !o.Drained {
		waiting = append(waiting, "node not drained from inference")
	}
	switch {
	case o.ForeignGPUWork && o.Drained:
		waiting = append(waiting, "the gpus are still held after the dapi stopped the node: a stray process, or the mlnode under this node id runs on another machine")
	case o.ForeignGPUWork:
		waiting = append(waiting, "foreign work on the gpus")
	}
	if !o.HasImage(d.BaseImage) {
		waiting = append(waiting, "base image not pulled")
	}
	if !o.MeshKey || !o.MeshIdentity {
		waiting = append(waiting, "no mesh identity")
	}
	return strings.Join(waiting, ", ")
}
