package run

import (
	"errors"
	"fmt"
	"slices"

	"trainshard/internal/domain/shared/vo"
)

type ActionKind string

const (
	ActionDrainNode          ActionKind = "drain_node"
	ActionPullImage          ActionKind = "pull_image"
	ActionVerifyImage        ActionKind = "verify_image"
	ActionCreateMeshIdentity ActionKind = "create_mesh_identity"
	ActionApplyMeshConfig    ActionKind = "apply_mesh_config"
	ActionCreateContainer    ActionKind = "create_container"
	ActionReplaceContainer   ActionKind = "replace_container"
	ActionStartContainer     ActionKind = "start_container"
	ActionStopContainer      ActionKind = "stop_container"
	ActionRemoveContainer    ActionKind = "remove_container"
	ActionRemoveMesh         ActionKind = "remove_mesh"
	ActionWipeVolumes        ActionKind = "wipe_volumes"
	ActionKillGPUProcesses   ActionKind = "kill_gpu_processes"
	ActionReturnNode         ActionKind = "return_node"
	ActionForgetRun          ActionKind = "forget_run"
)

type Action struct {
	Kind  ActionKind
	Image vo.ImageDigest
}

// ActionFailed is the step a converge stopped at
type ActionFailed struct {
	Kind  ActionKind
	cause error
}

func (e *ActionFailed) Error() string { return fmt.Sprintf("%s: %v", e.Kind, e.cause) }

func (e *ActionFailed) Unwrap() error { return e.cause }

// the steps that act on the run the mesh carries, never on the mesh itself
var runSteps = []ActionKind{ActionVerifyImage, ActionCreateContainer, ActionReplaceContainer, ActionStartContainer, ActionStopContainer}

// FailedOnTheRun says whether a converge got past the mesh and fell over on the run, which is
// the tenant's to fix with a deploy and says nothing about the peer list the node was given
func FailedOnTheRun(err error) bool {
	var failed *ActionFailed
	return errors.As(err, &failed) && slices.Contains(runSteps, failed.Kind)
}
