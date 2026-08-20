package run

import "trainshard/internal/domain/shared/vo"

type ActionKind string

const (
	ActionDrainNode          ActionKind = "drain_node"
	ActionPullImage          ActionKind = "pull_image"
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
)

type Action struct {
	Kind  ActionKind
	Image vo.ImageDigest
}
