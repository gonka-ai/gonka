package mesh

import "trainshard/internal/domain/shared"

var (
	ErrNoMembers        = shared.New("MESH_NO_MEMBERS", shared.ErrValidation, "mesh has no members")
	ErrDuplicateNode    = shared.New("MESH_DUPLICATE_NODE", shared.ErrValidation, "mesh has a duplicate node")
	ErrIncompleteMember = shared.New("MESH_INCOMPLETE_MEMBER", shared.ErrValidation, "mesh member is missing an address or a public key")
	ErrNodeNotInMesh    = shared.New("MESH_NODE_NOT_IN_MESH", shared.ErrValidation, "node is not part of the mesh")
	ErrRankOffMesh      = shared.New("MESH_RANK_OFF_MESH", shared.ErrValidation, "rank has no address on the mesh")
	ErrForeignIdentity  = shared.New("MESH_FOREIGN_IDENTITY", shared.ErrForbidden, "mesh member is not signed by the host that holds the node")
	ErrMissingIdentity  = shared.New("MESH_MISSING_IDENTITY", shared.ErrConflict, "a reserved node has no mesh identity yet")
	ErrMissingConfig    = shared.New("MESH_MISSING_CONFIG", shared.ErrConflict, "a reserved node has no peer list yet")
)
