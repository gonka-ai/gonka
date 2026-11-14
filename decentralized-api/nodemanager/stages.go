package nodemanager

import commonnm "common/nodemanager"

// Stage names for the node-selection log lines. Aliases of the shared
// common/nodemanager constants so production dapi and mock-dapi emit the same
// Loki join keys.
const (
	StageMLNodeAcquire = commonnm.StageMLNodeAcquire
	StageMLNodeRelease = commonnm.StageMLNodeRelease
)
